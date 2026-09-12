package sessionapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	llmapi "github.com/gorenx/goren/apiproxy/llm"
	"github.com/gorenx/goren/apiproxy/rpc"
	imageapi "github.com/gorenx/goren/apiproxy/session/image"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type promptAgent struct {
	Agent
	mu       sync.Mutex
	messages []agentmessage.UserMessage
}

func (*promptAgent) ID() session.SessionID        { return "conversation" }
func (*promptAgent) ParentID() *session.SessionID { return nil }
func (*promptAgent) Role() agent.Role             { return agent.OrdinaryRole }
func (subject *promptAgent) Followup(ctx context.Context, input agentmessage.UserMessage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	subject.mu.Lock()
	defer subject.mu.Unlock()
	subject.messages = append(subject.messages, input)
	return nil
}
func (subject *promptAgent) Steer(ctx context.Context, input agentmessage.UserMessage) error {
	return subject.Followup(ctx, input)
}
func (subject *promptAgent) Pending(context.Context) (agent.Pending, error) {
	subject.mu.Lock()
	defer subject.mu.Unlock()
	return agent.Pending{
		NextTurn: slices.Clone(subject.messages),
	}, nil
}

type promptDirectory struct {
	Agents
	subject Agent
}

func (directory promptDirectory) Resolve(context.Context, session.SessionID) (Agent, error) {
	return directory.subject, nil
}

type promptModels struct {
	Models
	mu         sync.Mutex
	modalities []llm.ModelModality
	configured int
}

func (*promptModels) Configuration(context.Context, session.SessionID) (llm.CallConfig, error) {
	return llm.CallConfig{
		Provider: "fixture",
		Model:    "vision",
	}, nil
}
func (*promptModels) ReadCatalog(context.Context) (llmapi.ProviderCatalog, error) {
	return llmapi.ProviderCatalog{
		Active: []llm.ProviderInfo{{
			ID: "fixture",
		}},
	}, nil
}
func (directory *promptModels) ResolveModelInfo(_ context.Context, _ string, identifier string) (llm.ResolvedModelInfo, error) {
	modes := directory.modalities
	if identifier == "text-only" {
		modes = []llm.ModelModality{llm.ModalityText}
	}
	return llm.ResolvedModelInfo{
		ModelInfo: llm.ModelInfo{
			Provider:        "fixture",
			ID:              identifier,
			InputModalities: modes,
		},
	}, nil
}
func (*promptModels) ResolveCallConfig(_ context.Context, proposed llm.CallConfig) (llm.CallConfig, error) {
	return proposed, nil
}
func (directory *promptModels) Configure(context.Context, session.SessionID, llm.CallConfig) error {
	directory.mu.Lock()
	defer directory.mu.Unlock()
	directory.configured++
	return nil
}

type promptStorage struct {
	imageapi.ImageReader
	calls   int
	inputs  []image.ImageInput
	started chan struct{}
	release chan struct{}
	failure error
}

func (storage *promptStorage) SaveImages(ctx context.Context, inputs []image.ImageInput) ([]image.ImageRef, error) {
	storage.calls++
	storage.inputs = inputs
	if storage.started != nil {
		close(storage.started)
	}
	if storage.release != nil {
		<-storage.release
	}
	// Intentionally ignore cancellation: the API must check before submission.
	if storage.failure != nil {
		return nil, storage.failure
	}
	refs := make([]image.ImageRef, len(inputs))
	for index, input := range inputs {
		refs[index] = image.ImageRef{
			AttachmentID: attachment.AttachmentID("picture-" + input.Name),
			MediaType:    input.MediaType,
			Width:        1,
			Height:       1,
			Bytes:        int64(len(input.Data)),
			Name:         input.Name,
		}
	}
	return refs, nil
}

func promptGateway(subject *promptAgent, directory *promptModels, storage *promptStorage) *Gateway {
	access := &sessionAccess{
		runtimeSessions: &activation{
			agents: promptDirectory{
				subject: subject,
			},
		},
	}
	return &Gateway{
		conversationUseCases: &sessionConversation{
			access:  access,
			models:  directory,
			storage: storage,
		},
		modelUseCases: &sessionModels{
			access: access,
			models: directory,
			sessions: listLiveStore{conversation: &Snapshot{
				Header: session.Header{
					ID: "conversation",
				},
			}},
		},
	}
}

func promptCatalog(t *testing.T, owner *Gateway) *rpc.Catalog {
	t.Helper()
	methods := rpc.NewCatalog()
	if err := rpc.RegisterUnary(methods, PromptMethod, DecodePromptRequest, owner.Prompt); err != nil {
		t.Fatal(err)
	}
	if err := rpc.RegisterUnary(methods, SelectModelMethod, DecodeSelectModelRequest, owner.SelectModel); err != nil {
		t.Fatal(err)
	}
	return methods
}

const mixedImagePrompt = `{"sessionId":"conversation","mode":"queue","content":[{"type":"text","text":"before"},{"type":"image","mediaType":"image/png","data":"AQ==","name":"first.png"},{"type":"text","text":"between"},{"type":"image","mediaType":"image/png","data":"Ag==","name":"second.png"}]}`

func TestPromptImageModalityAndOrderedBatch(t *testing.T) {
	for _, sample := range []struct {
		name     string
		modes    []llm.ModelModality
		rejected bool
	}{
		{
			name: "unspecified",
		},
		{
			name:     "explicit empty",
			modes:    []llm.ModelModality{},
			rejected: true,
		},
		{
			name:     "text only",
			modes:    []llm.ModelModality{llm.ModalityText},
			rejected: true,
		},
		{
			name:  "vision",
			modes: []llm.ModelModality{llm.ModalityImage},
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			subject := &promptAgent{}
			directory := &promptModels{
				modalities: sample.modes,
			}
			storage := &promptStorage{}
			owner := promptGateway(subject, directory, storage)
			result, err := promptCatalog(t, owner).DispatchUnary(context.Background(), PromptMethod, "request", []byte(mixedImagePrompt))
			if err != nil {
				t.Fatal(err)
			}
			if sample.rejected {
				if result.OK || result.Error == nil || result.Error.Code != connection.ErrorAttachment || !strings.Contains(string(result.Error.Details), "MODEL_DOES_NOT_SUPPORT_IMAGES") || storage.calls != 0 || len(subject.messages) != 0 {
					t.Fatalf("modality rejection: %+v", result)
				}
				return
			}
			if !result.OK || storage.calls != 1 || len(storage.inputs) != 2 || len(subject.messages) != 1 {
				t.Fatalf("batch result: %+v", result)
			}
			blocks := subject.messages[0].ContentValue()
			if len(blocks) != 4 || blocks[0].(agentmessage.TextBlock).Text != "before" || blocks[2].(agentmessage.TextBlock).Text != "between" || blocks[1].(agentmessage.ImageBlock).Attachment.Name != "first.png" || blocks[3].(agentmessage.ImageBlock).Attachment.Name != "second.png" {
				t.Fatalf("content order: %+v", blocks)
			}
			if storage.inputs[0].Data[0] != 1 || storage.inputs[1].Data[0] != 2 {
				t.Fatal("base64 input order")
			}
		})
	}
}

func TestImageAdmissionBlocksModelSwitchUntilMessageCommit(t *testing.T) {
	subject := &promptAgent{}
	directory := &promptModels{
		modalities: []llm.ModelModality{llm.ModalityImage},
	}
	storage := &promptStorage{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	owner := promptGateway(subject, directory, storage)
	methods := promptCatalog(t, owner)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	promptDone := make(chan connection.RPCResult, 1)
	go func() {
		result, _ := methods.DispatchUnary(ctx, PromptMethod, "prompt", []byte(mixedImagePrompt))
		promptDone <- result
	}()
	select {
	case <-storage.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	selected := make(chan connection.RPCResult, 1)
	go func() {
		result, _ := methods.DispatchUnary(ctx, SelectModelMethod, "select", []byte(`{"sessionId":"conversation","provider":"fixture","model":"text-only"}`))
		selected <- result
	}()
	waitAdmissionCount(t, &owner.conversationUseCases.access.admissions, 2)
	select {
	case result := <-selected:
		t.Fatalf("selection overtook admission: %+v", result)
	default:
	}
	close(storage.release)
	if result := <-promptDone; !result.OK {
		t.Fatalf("prompt failed: %+v", result)
	}
	if result := <-selected; result.OK || result.Error == nil || result.Error.Code != connection.ErrorModelUnavailable {
		t.Fatalf("text model accepted after image commit: %+v", result)
	}
	if directory.configured != 0 {
		t.Fatal("model changed despite committed image")
	}
}

func TestFailedOrCanceledImageBatchDoesNotSubmit(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		subject := &promptAgent{}
		storage := &promptStorage{}
		ctx, stop := context.WithCancel(context.Background())
		if canceled {
			storage.started = make(chan struct{})
			storage.release = make(chan struct{})
		} else {
			storage.failure = &attachment.Error{
				Code:    image.InvalidImage,
				Message: "invalid image",
			}
		}
		owner := promptGateway(subject, &promptModels{}, storage)
		methods := promptCatalog(t, owner)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = methods.DispatchUnary(ctx, PromptMethod, "prompt", []byte(mixedImagePrompt))
		}()
		if canceled {
			select {
			case <-storage.started:
			case <-time.After(5 * time.Second):
				t.Fatal("image admission did not start")
			}
			stop()
			close(storage.release)
		}
		<-done
		stop()
		if len(subject.messages) != 0 {
			t.Fatal("failed/canceled batch entered Agent inbox")
		}
	}
}

func waitAdmissionCount(t *testing.T, queue *admissionQueue, want int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		queue.mu.Lock()
		got := len(queue.tickets["conversation"])
		queue.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("queued %d, want %d", got, want)
		case <-ticker.C:
		}
	}
}

func TestAdmissionQueueCanceledWaiterCannotReleaseActiveOperation(t *testing.T) {
	var queue admissionQueue
	first, err := queue.acquire(context.Background(), "conversation")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		release, err := queue.acquire(ctx, "conversation")
		if release != nil {
			release()
		}
		abandoned <- err
	}()
	waitAdmissionCount(t, &queue, 2)
	next := make(chan func(), 1)
	go func() { release, _ := queue.acquire(context.Background(), "conversation"); next <- release }()
	waitAdmissionCount(t, &queue, 3)
	stop()
	if err := <-abandoned; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-next:
		t.Fatal("canceled waiter advanced past active operation")
	default:
	}
	first()
	release := <-next
	release()
	release()
	waitAdmissionCount(t, &queue, 0)
}
