package pool

import (
	"context"

	"github.com/sudesh856/LoadForge/internal/worker"
)
type Pool struct {
	jobs chan worker.Job
	results chan worker.Result
}

func New(bufferSize int) *Pool {
	return &Pool{
		jobs: make(chan worker.Job, bufferSize),
		results: make(chan worker.Result, bufferSize),
	}
}


func(p *Pool) Start(ctx context.Context, n int) {
	for i := 0; i <n; i++ {
		go worker.RunWorker(ctx, p.jobs, p.results)
	}
}
func(p *Pool) Submit(job worker.Job) {
	p.jobs <- job
}

func (p *Pool) Results() <-chan worker.Result {
	return p.results
}

func (p *Pool) Close() {
	close(p.jobs)
}