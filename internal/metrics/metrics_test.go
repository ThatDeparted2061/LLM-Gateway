package metrics

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

func TestStatus(t *testing.T) {
	for err, want := range map[error]string{
		nil: "ok",
		fmt.Errorf("ollama: %w", &providers.StatusError{Code: 429}): "429",
		fmt.Errorf("ollama: %w", context.Canceled):                  "canceled",
		errors.New("connection refused"):                            "error",
	} {
		if got := status(err); got != want {
			t.Errorf("status(%v) = %q, want %q", err, got, want)
		}
	}
}
