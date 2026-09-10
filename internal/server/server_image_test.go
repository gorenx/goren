package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	raster "image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm/deepseek"
)

func TestHTTPImageReachesDeepSeekAndCanBeRead(t *testing.T) {
	received := make(chan string, 4)
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		var payload struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(incoming.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(payload.Messages), "image_url") {
			received <- string(payload.Messages)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a picture\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer remote.Close()
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	root := t.TempDir()
	settings := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider: deepseek.Config{
			BaseURL: &remote.URL,
		},
	}
	process, err := New(settings, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer process.Close(context.Background())
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"pictures"}`))
	callHTTP(t, ctx, process, "session.selectModel", json.RawMessage(`{"sessionId":"pictures","provider":"deepseek-official","model":"deepseek-v4-flash-vision-exp"}`))
	var source bytes.Buffer
	if err := png.Encode(&source, raster.NewNRGBA(raster.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(source.Bytes())
	digest := sha256.Sum256(source.Bytes())
	identifier := "sha256:" + hex.EncodeToString(digest[:])
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(fmt.Sprintf(`{"sessionId":"pictures","mode":"queue","content":[{"type":"text","text":"describe"},{"type":"image","mediaType":"image/png","data":"%s","name":"picture.png"}]}`, encoded)))

	select {
	case content := <-received:
		if !strings.Contains(content, "data:image/png;base64,"+encoded) {
			t.Fatal(content)
		}
	case <-ctx.Done():
		t.Fatal("image never reached upstream")
	}
	readPayload := json.RawMessage(fmt.Sprintf(`{"sessionId":"pictures","attachmentId":"%s"}`, identifier))
	reply := callHTTP(t, ctx, process, "session.attachment", readPayload)
	if !strings.Contains(string(reply), `"data":"`+encoded+`"`) {
		t.Fatalf("read: %s", reply)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := New(settings, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer restored.Close(context.Background())
	replay := callHTTP(t, ctx, restored, "session.attachment", readPayload)
	if string(replay) != string(reply) {
		t.Fatalf("restarted read=%s want=%s", replay, reply)
	}
}
