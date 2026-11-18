package httpkit

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestServeContextRespectsCallerCancellation(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		ServeContext(ctx, "shutdown-test", "0", http.NewServeMux())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server ignored caller cancellation")
	}
}
