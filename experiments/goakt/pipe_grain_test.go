package spike

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type fileRead struct {
	Path       string
	Gate       <-chan struct{}
	Entered    chan<- struct{}
	CancelSeen chan<- struct{}
}
type fileDone struct {
	Text      string
	Failure   string
	Cancelled bool
}
type fileClose struct{}
type fileState struct {
	Phase       string
	Closing     bool
	Settlements int
}

// FileGrain owns one operation and its reply. The task captures immutable
// inputs and its own cancellation context; only OnReceive changes state.
// The gate models non-interruptible work, not cancellability of os.ReadFile.
type FileGrain struct {
	pending      *actor.GrainReply
	closingReply *actor.GrainReply
	cancelTask   context.CancelFunc
	state        fileState
}

func (owner *FileGrain) OnActivate(context.Context, *actor.GrainProps) error {
	owner.state.Phase = "idle"
	return nil
}
func (owner *FileGrain) OnDeactivate(context.Context, *actor.GrainProps) error {
	// Test cleanup opens gates before stopping the system. A production close
	// must first obtain Quiesced; this hook is not a task-join implementation.
	if owner.cancelTask != nil {
		owner.cancelTask()
	}
	return nil
}
func (owner *FileGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case fileRead:
		if owner.state.Closing || owner.state.Phase != "idle" {
			gc.Response("rejected")
			return
		}
		owner.state.Phase = "reading"
		owner.pending = gc.DeferResponse()
		operationContext, stop := context.WithCancel(context.Background())
		owner.cancelTask = stop
		failure := gc.PipeToSelf(func() (any, error) {
			if incoming.Entered != nil {
				incoming.Entered <- struct{}{}
			}
			if incoming.Gate != nil {
				select {
				case <-incoming.Gate:
				case <-operationContext.Done():
					if incoming.CancelSeen != nil {
						incoming.CancelSeen <- struct{}{}
					}
					<-incoming.Gate // Work cannot exit until the test releases the effect.
				}
			}
			content, readFailure := os.ReadFile(incoming.Path)
			completion := fileDone{
				Text:      string(content),
				Cancelled: operationContext.Err() != nil,
			}
			if readFailure != nil {
				completion.Failure = readFailure.Error()
			}
			// A typed result preserves operation outcome, including ordinary I/O errors.
			return completion, nil
		})
		if failure != nil {
			stop()
			owner.state.Phase = "idle"
			owner.pending.Err(failure)
		}
	case requestBusinessCancel:
		if owner.cancelTask != nil && owner.state.Phase == "reading" {
			owner.state.Phase = "cancelling"
			owner.cancelTask()
		}
		gc.Response("cancel-accepted")
	case fileDone:
		if owner.state.Phase != "reading" && owner.state.Phase != "cancelling" {
			return
		}
		owner.cancelTask()
		owner.state.Settlements++
		owner.state.Phase = "idle"
		owner.pending.Response(incoming)
		owner.pending = nil
		if owner.state.Closing {
			owner.state.Phase = "quiesced"
			owner.closingReply.Response("quiesced")
		}
	case fileClose:
		if owner.state.Closing {
			gc.Response("already-closing")
			return
		}
		owner.state.Closing = true
		if owner.state.Phase == "idle" {
			owner.state.Phase = "quiesced"
			gc.Response("quiesced")
			return
		}
		owner.closingReply = gc.DeferResponse()
	case requestProbe:
		gc.Response(owner.state)
	}
}
func makeFileGrain(t *testing.T, running actor.ActorSystem) *actor.GrainIdentity {
	t.Helper()
	identity, failure := actor.GrainOf[*FileGrain](context.Background(), running, "file-tool",
		actor.WithLongLivedGrain(),
		actor.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if failure != nil {
		t.Fatal(failure)
	}
	return identity
}
func TestPipeToSelfReadsFileAndReturnsIOFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{
			false: "read",
			true:  "missing",
		}[missing], func(t *testing.T) {
			fixture := newRequestHarness(t)
			identity := makeFileGrain(t, fixture.system)
			path := filepath.Join(t.TempDir(), "input.txt")
			if !missing {
				if failure := os.WriteFile(path, []byte("文件内容\n"), 0600); failure != nil {
					t.Fatal(failure)
				}
			}
			sendRequestMessage(t, fixture.client, grainRequestStart{
				Target: identity,
				Payload: fileRead{
					Path: path,
				},
				Timeout: 2 * time.Second,
			})
			outcome := readRequestOutcome(t, fixture.outcomes)
			if outcome.Err != nil {
				t.Fatal(outcome.Err)
			}
			completion := outcome.Payload.(fileDone)
			if missing {
				if completion.Failure == "" {
					t.Fatal("missing file reported success")
				}
			} else if completion.Text != "文件内容\n" || completion.Failure != "" || completion.Cancelled {
				t.Fatalf("read result: %+v", completion)
			}
			observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(fileState)
			if observed.Phase != "idle" || observed.Settlements != 1 {
				t.Fatalf("settlement: %+v", observed)
			}
		})
	}
}
func TestPipeToSelfCancelAndCloseWaitForTaskCompletion(t *testing.T) {
	fixture := newRequestHarness(t)
	identity := makeFileGrain(t, fixture.system)
	path := filepath.Join(t.TempDir(), "input.txt")
	if failure := os.WriteFile(path, []byte("finished effect"), 0600); failure != nil {
		t.Fatal(failure)
	}
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	entered, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target: identity,
		Payload: fileRead{
			Path:       path,
			Gate:       gate,
			Entered:    entered,
			CancelSeen: cancelled,
		},
		Timeout: 3 * time.Second,
	})
	awaitSignal(t, entered)
	if observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(fileState); observed.Phase != "reading" {
		t.Fatalf("blocked receive: %+v", observed)
	}
	closeFeed := make(chan requestOutcome, 2)
	closer := spawnRequestActor(t, fixture.system, "closer", &requestClient{
		outcomes: closeFeed,
	})
	sendRequestMessage(t, closer, grainRequestStart{
		Target:  identity,
		Payload: fileClose{},
		Timeout: 3 * time.Second,
	})
	deadline := time.Now().Add(time.Second)
	for !inspectGrain(t, fixture.system, identity, requestProbe{}).(fileState).Closing {
		if time.Now().After(deadline) {
			t.Fatal("close not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if reply := inspectGrain(t, fixture.system, identity, fileRead{
		Path: path,
	}); reply != "rejected" {
		t.Fatal("new work admitted while closing")
	}
	inspectGrain(t, fixture.system, identity, requestBusinessCancel{})
	awaitSignal(t, cancelled)
	observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(fileState)
	if observed.Phase != "cancelling" || observed.Settlements != 0 {
		t.Fatalf("settled before task exited: %+v", observed)
	}
	if count := ask(t, closer, requestProbe{}); count != 0 {
		t.Fatal("closed before task exited")
	}
	if count := ask(t, fixture.client, requestProbe{}); count != 0 {
		t.Fatal("replied before task exited")
	}
	release()
	outcome := readRequestOutcome(t, fixture.outcomes)
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	completion := outcome.Payload.(fileDone)
	if !completion.Cancelled || completion.Text != "finished effect" {
		t.Fatalf("lost effect or cancellation: %+v", completion)
	}
	closed := readRequestOutcome(t, closeFeed)
	if closed.Err != nil || closed.Payload != "quiesced" {
		t.Fatalf("close: %+v", closed)
	}
	observed = inspectGrain(t, fixture.system, identity, requestProbe{}).(fileState)
	if observed.Phase != "quiesced" || observed.Settlements != 1 {
		t.Fatalf("final state: %+v", observed)
	}
}
