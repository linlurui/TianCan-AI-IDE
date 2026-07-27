package ai

import (
	"context"
	"testing"
	"time"
)

func TestCallLLMWithTimeoutCancelsProviderContext(t *testing.T) {
	cancelled := make(chan struct{})
	start := time.Now()

	_, err := callLLMWithTimeout(context.Background(), 30*time.Millisecond, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		close(cancelled)
		return "", ctx.Err()
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout returned too slowly: %s", elapsed)
	}

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("provider context was not cancelled")
	}
}

func TestStopAgentRequestCancelsActiveContext(t *testing.T) {
	service := NewService()
	ctx, requestID := service.beginRequest()

	if !service.StopAgentRequest() {
		t.Fatal("expected active request to be stopped")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active request context was not cancelled")
	}

	service.endRequest(requestID)
	if service.StopAgentRequest() {
		t.Fatal("expected no active request after cancellation")
	}
}
