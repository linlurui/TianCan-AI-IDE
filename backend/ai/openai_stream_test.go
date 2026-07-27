package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatOpenAIStreamCollectsSSETokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"你"}}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"好"}}]}`)
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	service := NewService()
	got, err := service.chatOpenAIStream(context.Background(), server.URL+"/v1", "key", "model", []ChatMessage{{Role: "user", Content: "hi"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "你好" {
		t.Fatalf("unexpected streamed response: %q", got)
	}
}

func TestChatOpenAIStreamAcceptsJSONFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"choices":[{"message":{"role":"assistant","content":"fallback"}}]}`)
	}))
	defer server.Close()

	service := NewService()
	got, err := service.chatOpenAIStream(context.Background(), server.URL, "", "model", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Fatalf("unexpected fallback response: %q", got)
	}
}
