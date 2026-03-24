package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/sudesh856/LoadForge/internal/worker"
)

func TestPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
		w.WriteHeader(http.StatusOK)
	}))

	defer server.Close()

	ctx := context.Background()
	p := New(100)
	p.Start(ctx, 5)

	for i := 0; i < 10; i++ {
		p.Submit(worker.Job{URL: server.URL})
	}

	//collecting 10 results

	for i := 0; i < 10; i++ {
		result := <-p.Results()
		if result.Err != nil {
			 t.Errorf("Unexpected error: %v", result.Err)
		}

		if result.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", result.StatusCode)
		}

		t.Logf("Result %d -- status: %d latency: %v", i+1, result.StatusCode, result.Latency)

	}
}