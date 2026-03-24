package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	defer server.Close()


	jobs := make(chan Job, 10)
	results := make(chan Result, 10)
	ctx := context.Background()

	//launcing 5 workers

	for i := 0; i <5; i++{
		go RunWorker(ctx, jobs, results)
	}

	for i := 0; i <5; i++ {
		jobs <- Job{URL: server.URL}
	}

	//sending 5 jobs
	for i := 0; i<5; i++ {
		result := <-results
		if result.Err != nil {
			t.Errorf("Unexpected error: %v", result.Err)
		} 

		if result.StatusCode !=  http.StatusOK {
			t.Errorf("Expected 200, got %d", result.StatusCode)
		}
		t.Logf("Result %d -- status: %d latency: %v", i+1, result.StatusCode, result.Latency)
	}
}