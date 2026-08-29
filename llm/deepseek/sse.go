package deepseek

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gorenx/goren/llm"
)

const donePayload = "[DONE]"

type payloadStream interface {
	NextPayload(context.Context) (string, bool, error)
	Close(context.Context) error
}

type ssePayloadStream struct {
	body        io.ReadCloser
	reader      *bufio.Reader
	idleTimeout time.Duration
	onComment   func(string)

	nextMu    sync.Mutex
	stateMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
	closed    bool
	done      bool
	firstLine bool
	dataLines []string
}

type lineResult struct {
	line string
	err  error
}

func newSSEPayloadStream(body io.ReadCloser, idleTimeout time.Duration, onComment func(string)) (payloadStream, error) {
	if body == nil {
		return nil, errors.New("llm-deepseek: SSE response body is nil")
	}
	if idleTimeout <= 0 {
		return nil, errors.New("llm-deepseek: SSE idle timeout must be positive")
	}
	return &ssePayloadStream{
		body: body, reader: bufio.NewReader(body), idleTimeout: idleTimeout,
		onComment: onComment, firstLine: true,
	}, nil
}

func (streamState *ssePayloadStream) NextPayload(requestContext context.Context) (string, bool, error) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	streamState.nextMu.Lock()
	defer streamState.nextMu.Unlock()
	if err := requestContext.Err(); err != nil {
		return "", false, err
	}
	streamState.stateMu.Lock()
	if streamState.closed || streamState.done {
		streamState.stateMu.Unlock()
		return "", false, nil
	}
	streamState.stateMu.Unlock()

	for {
		line, err := streamState.readLine(requestContext)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", false, llm.MustLlmError("SSE stream ended without [DONE]", "STREAM_CLOSED")
			}
			return "", false, err
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if streamState.firstLine {
			streamState.firstLine = false
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if line == "" {
			if len(streamState.dataLines) == 0 {
				continue
			}
			payload := strings.Join(streamState.dataLines, "\n")
			streamState.dataLines = streamState.dataLines[:0]
			if payload == donePayload {
				streamState.stateMu.Lock()
				streamState.done = true
				streamState.stateMu.Unlock()
			}
			return payload, true, nil
		}
		if strings.HasPrefix(line, ":") {
			if streamState.onComment != nil {
				comment := strings.TrimPrefix(line, ":")
				comment = strings.TrimPrefix(comment, " ")
				streamState.onComment(comment)
			}
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if hasColon {
			value = strings.TrimPrefix(value, " ")
		}
		if field == "data" {
			streamState.dataLines = append(streamState.dataLines, value)
		}
	}
}

func (streamState *ssePayloadStream) readLine(requestContext context.Context) (string, error) {
	resultChannel := make(chan lineResult, 1)
	go func() {
		line, err := streamState.reader.ReadString('\n')
		resultChannel <- lineResult{
			line: line,
			err:  err,
		}
	}()
	timer := time.NewTimer(streamState.idleTimeout)
	defer timer.Stop()
	select {
	case result := <-resultChannel:
		if result.err != nil {
			return "", result.err
		}
		return result.line, nil
	case <-requestContext.Done():
		_ = streamState.closeBody()
		return "", requestContext.Err()
	case <-timer.C:
		_ = streamState.closeBody()
		if err := requestContext.Err(); err != nil {
			return "", err
		}
		return "", llm.MustLlmError(
			fmt.Sprintf("DeepSeek stream idle timeout after %dms", streamState.idleTimeout.Milliseconds()),
			"TIMEOUT",
		)
	}
}

func (streamState *ssePayloadStream) Close(context.Context) error {
	streamState.stateMu.Lock()
	streamState.closed = true
	streamState.stateMu.Unlock()
	return streamState.closeBody()
}

func (streamState *ssePayloadStream) closeBody() error {
	streamState.closeOnce.Do(func() {
		streamState.closeErr = streamState.body.Close()
	})
	return streamState.closeErr
}
