package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Job struct {
	Name           string
	URL            string
	Method         string
	Body           string
	Headers        map[string]string
	ExpectedStatus int
	Timeout        time.Duration
	BasicAuth      string
}

type Result struct {
	Latency      time.Duration
	StatusCode   int
	Err          error
	Bytes        int64
	Body         []byte
	EndpointName string
}

// one shared client per worker goroutine — created once, reused forever
var sharedTransport = &http.Transport{
	ForceAttemptHTTP2:     true,
	DisableKeepAlives:     false,
	MaxIdleConns:          1000,
	MaxIdleConnsPerHost:   1000,
	MaxConnsPerHost:       0,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

func RunWorker(ctx context.Context, jobs <-chan Job, results chan<- Result) {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: sharedTransport,
	}

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}

			if job.Timeout > 0 {
				client.Timeout = job.Timeout
			}

			start := time.Now()

			method := job.Method
			if method == "" {
				method = "GET"
			}

			var bodyReader io.Reader
			if job.Body != "" {
				bodyReader = strings.NewReader(job.Body)
			}

			req, err := http.NewRequestWithContext(ctx, method, job.URL, bodyReader)
			if err != nil {
				results <- Result{Latency: time.Since(start), Err: err}
				continue
			}

			for k, v := range job.Headers {
				req.Header.Set(k, v)
			}

			if job.BasicAuth != "" {
				parts := strings.SplitN(job.BasicAuth, ":", 2)
				if len(parts) == 2 {
					req.SetBasicAuth(parts[0], parts[1])
				}
			}

			resp, err := client.Do(req)
			latency := time.Since(start)

			if err != nil {
				results <- Result{Latency: latency, Err: err}
				continue
			}

			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var resultErr error
			if job.ExpectedStatus != 0 && resp.StatusCode != job.ExpectedStatus {
				resultErr = fmt.Errorf("expected status %d got %d", job.ExpectedStatus, resp.StatusCode)
			}

			results <- Result{
				Latency:      latency,
				StatusCode:   resp.StatusCode,
				Bytes:        int64(len(bodyBytes)),
				Body:         bodyBytes,
				EndpointName: job.Name,
				Err:          resultErr,
			}
		}
	}
}