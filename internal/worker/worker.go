package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	
)

//since Job is what we send to a worker which is  the url to hit;

type Job struct {
	Name           string
	URL            string
	Method         string
	Body           string
	Headers        map[string]string
	ExpectedStatus int
	Timeout        time.Duration
}


//and since Result is what the worker sends back after firing a request

type Result struct {
	Latency      time.Duration
	StatusCode   int
	Err          error
	Bytes        int64
	Body         []byte
	EndpointName string
}

//listening by RunWorker for jobs and firing HTTP requests

func RunWorker(ctx context.Context, jobs <-chan Job, results chan<- Result) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}

			// per-request timeout
			timeout := job.Timeout
			if timeout == 0 {
				timeout = 10 * time.Second
			}

			// HTTP/2 + keep-alive
			client := &http.Client{
				Timeout: timeout,
				Transport: &http.Transport{
					ForceAttemptHTTP2:   true,
					DisableKeepAlives:   false,
					MaxIdleConns:        100,
					IdleConnTimeout:     90 * time.Second,
					TLSHandshakeTimeout: 10 * time.Second,
				},
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

			// add headers from job
			for k, v := range job.Headers {
				req.Header.Set(k, v)
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
	