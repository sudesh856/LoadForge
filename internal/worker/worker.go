package worker

import (
	"context"
	"net/http"
	"time"
)

//since Job is what we send to a worker which is  the url to hit;

type Job struct {
	URL string
}


//and since Result is what the worker sends back after firing a request

type Result struct {
	Latency time.Duration
	StatusCode int
	Err error
	Bytes int64
}

//listening by RunWorker for jobs and firing HTTP requests

func RunWorker(ctx context.Context, jobs <- chan Job, results chan <- Result) {

	client := &http.Client{Timeout: 10 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return //the context is now cancelled, stop worker.
		case job,ok := <-jobs:
			if !ok {
				return //job channel is now closed, stop worker.
			}

			start := time.Now()
			resp, err := client.Get(job.URL)
			latency := time.Since(start)

			if err != nil {
				results <- Result {Latency: latency, Err: err}
				continue }

					results <- Result{
						Latency: latency,
						StatusCode: resp.StatusCode,
						Bytes: resp.ContentLength,
					}
					resp.Body.Close()
				}

				
			}
		}
	