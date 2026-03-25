package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"
	"strings"

	"encoding/json"

	"github.com/spf13/cobra"
	"github.com/sudesh856/LoadForge/internal/metrics"
	"github.com/sudesh856/LoadForge/internal/pool"
	"github.com/sudesh856/LoadForge/internal/reporter"
	"github.com/sudesh856/LoadForge/internal/worker"
	"golang.org/x/time/rate"
	"github.com/sudesh856/LoadForge/internal/ramp"
	"github.com/sudesh856/LoadForge/internal/scenario"
)

var url      string
var vus      int
var duration string
var rps      int
var output string
var flagScenarioFile string
var method  string
var headers []string
var timeout string




var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a load test",
	Run: func(cmd *cobra.Command, args []string) {

	if flagScenarioFile != "" {
    s, err := scenario.LoadScenario(flagScenarioFile)
    if err != nil {
        fmt.Println("scenario error:", err)
        return
    }

    // Convert stages for ramp controller
    rampStages := make([]ramp.Stage, len(s.Stages))
    for i, st := range s.Stages {
        rampStages[i] = ramp.Stage{
            Duration:  st.ParsedDuration,
            TargetVUs: st.TargetVUs,
        }
    }

    // Total duration = sum of all stages
    totalDur := time.Duration(0)
    for _, st := range s.Stages {
        totalDur += st.ParsedDuration
    }

    ctx, cancelSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	ctx, cancelTimeout := context.WithTimeout(ctx, totalDur)

	defer cancelTimeout()
	defer cancelSignal()

    ctrl := ramp.New(rampStages)
	go ctrl.Run(ctx)

	maxVUs := 0
	for _, st := range rampStages {
		if st.TargetVUs > maxVUs {
			maxVUs = st.TargetVUs
		}
	}
	p := pool.New(1000)
	p.Start(ctx, maxVUs)
    agg := metrics.New()
    agg.Start(p.Results())
    start := time.Now()

    var wg sync.WaitGroup
    wg.Add(1)
	chainStore := scenario.NewChainStore()
    go func() {
    for result := range p.Results() {
        if result.EndpointName != "" && len(result.Body) > 0 {
            for _, ep := range s.Endpoints {
                if ep.Name == result.EndpointName {
                    chainStore.Store(result.EndpointName, result.Body, ep.Extract)
                    break
                }
            }
        }
    }
}()

go func() {
    defer wg.Done()
    for {
        select {
        case <-ctx.Done():
            return
        case ctrl.Semaphore <- struct{}{}:
            ep := scenario.PickEndpoint(s.Endpoints)
            vars := chainStore.ToVars()
            url := scenario.ReplaceVars(ep.URL, vars)
            body := scenario.ReplaceVars(ep.Body, vars)
            p.Submit(worker.Job{
                Name:   ep.Name,
                URL:    url,
                Method: ep.Method,
                Body:   body,
				ExpectedStatus: ep.ExpectedStatus,

            })
            <-ctrl.Semaphore
        }
    }
}()

    <-ctx.Done()
    wg.Wait()
    time.Sleep(100 * time.Millisecond)

    elapsed := time.Since(start)
    if output == "json" {
        type ScenarioSummary struct {
            Scenario      string  `json:"scenario"`
            DurationSecs  float64 `json:"duration_secs"`
            TotalRequests int64   `json:"total_requests"`
            AvgRPS        float64 `json:"avg_rps"`
            P50           int64   `json:"p50_ms"`
            P75           int64   `json:"p75_ms"`
            P90           int64   `json:"p90_ms"`
            P95           int64   `json:"p95_ms"`
            P99           int64   `json:"p99_ms"`
            P999          int64   `json:"p999_ms"`
            Max           int64   `json:"max_ms"`
            Errors        int64   `json:"errors"`
            ErrorRate     float64 `json:"error_rate_pct"`
        }
        sum := ScenarioSummary{
            Scenario:      s.Name,
            DurationSecs:  elapsed.Seconds(),
            TotalRequests: agg.TotalRequests(),
            AvgRPS:        agg.RPS(elapsed),
            P50:           agg.P50(),
            P75:           agg.P75(),
            P90:           agg.P90(),
            P95:           agg.P95(),
            P99:           agg.P99(),
            P999:          agg.P999(),
            Max:           agg.Max(),
            Errors:        agg.ErrorCount(),
            ErrorRate:     agg.ErrorRate(),
        }
        data, _ := json.MarshalIndent(sum, "", "  ")
        fmt.Println("\n" + string(data))
    } else {
        fmt.Printf("\n\n----- SUDD SCENARIO SUMMARY -----\n")
        fmt.Printf("Scenario       : %s\n", s.Name)
        fmt.Printf("Duration       : %s\n", elapsed.Round(time.Second))
        fmt.Printf("-----------------------------------\n")
        fmt.Printf("Total Requests : %d\n", agg.TotalRequests())
        fmt.Printf("Avg RPS        : %.2f\n", agg.RPS(elapsed))
        fmt.Printf("-----------------------------------\n")
        fmt.Printf("p50            : %dms\n", agg.P50())
        fmt.Printf("p75            : %dms\n", agg.P75())
        fmt.Printf("p90            : %dms\n", agg.P90())
        fmt.Printf("p95            : %dms\n", agg.P95())
        fmt.Printf("p99            : %dms\n", agg.P99())
        fmt.Printf("p999           : %dms\n", agg.P999())
        fmt.Printf("Max            : %dms\n", agg.Max())
        fmt.Printf("-----------------------------------\n")
        fmt.Printf("Errors         : %d\n", agg.ErrorCount())
        fmt.Printf("Error Rate     : %.2f%%\n", agg.ErrorRate())
        fmt.Printf("=================================\n")
    }
    return

	
	}
		dur, err := time.ParseDuration(duration)
		if err != nil {
			fmt.Println("invalid duration:", err)
			return
		}

		// ctx cancelled on timeout OR ctrl+C
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		ctx, cancel = context.WithTimeout(ctx, dur)
		defer cancel()

		// rate limiter
		var limiter *rate.Limiter
		if rps > 0 {
			limiter = rate.NewLimiter(rate.Limit(rps), rps)
		}

		p := pool.New(1000)
		p.Start(ctx, vus)

		agg := metrics.New()
		agg.Start(p.Results())

		start := time.Now()

		// WaitGroup to track job submitter goroutine
		// parse headers
		headerMap := map[string]string{}
		for _, h := range headers {
			parts := strings.SplitN(h, ":", 2)
			if len(parts) == 2 {
				headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}

		// parse timeout
		reqTimeout, err := time.ParseDuration(timeout)
		if err != nil {
			reqTimeout = 10 * time.Second
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					if limiter != nil {
						limiter.Wait(ctx)
					}
					p.Submit(worker.Job{
						URL:     url,
						Method:  method,
						Headers: headerMap,
						Timeout: reqTimeout,
					})
				}
			}
		}()

		// live terminal output every second
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					elapsed := time.Since(start)
					reporter.Print(
						agg.TotalRequests(),
						agg.RPS(elapsed),
						agg.P99(),
						agg.ErrorCount(),
					)
				}
			}
		}()

		// wait for context to expire or ctrl+C
		<-ctx.Done()

		// wait for submitter goroutine to stop
		wg.Wait()

		// give aggregator a moment to process remaining results
		time.Sleep(100 * time.Millisecond)

		elapsed := time.Since(start)

		// final summary
		if output == "json" {
			type Summary struct {
				URL          string  `json:"url"`
				VUs          int     `json:"vus"`
				DurationSecs float64 `json:"duration_secs"`
				TotalRequests int64  `json:"total_requests"`
				AvgRPS       float64 `json:"avg_rps"`
				P50          int64   `json:"p50_ms"`
				P75          int64   `json:"p75_ms"`
				P90          int64   `json:"p90_ms"`
				P95          int64   `json:"p95_ms"`
				P99          int64   `json:"p99_ms"`
				P999         int64   `json:"p999_ms"`
				Max          int64   `json:"max_ms"`
				Errors       int64   `json:"errors"`
				ErrorRate    float64 `json:"error_rate_pct"`
			}
			s := Summary{
				URL:           url,
				VUs:           vus,
				DurationSecs:  elapsed.Seconds(),
				TotalRequests: agg.TotalRequests(),
				AvgRPS:        agg.RPS(elapsed),
				P50:           agg.P50(),
				P75:           agg.P75(),
				P90:           agg.P90(),
				P95:           agg.P95(),
				P99:           agg.P99(),
				P999:          agg.P999(),
				Max:           agg.Max(),
				Errors:        agg.ErrorCount(),
				ErrorRate:     agg.ErrorRate(),
			}
			data, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println("\n" + string(data))
		} else {
			fmt.Printf("\n\n----- SUDD LOAD TEST SUMMARY -----\n")
			fmt.Printf("URL            : %s\n", url)
			fmt.Printf("VUs            : %d\n", vus)
			fmt.Printf("Duration       : %s\n", elapsed.Round(time.Second))
			fmt.Printf("-----------------------------------\n")
			fmt.Printf("Total Requests : %d\n", agg.TotalRequests())
			fmt.Printf("Avg RPS        : %.2f\n", agg.RPS(elapsed))
			fmt.Printf("-----------------------------------\n")
			fmt.Printf("p50            : %dms\n", agg.P50())
			fmt.Printf("p75            : %dms\n", agg.P75())
			fmt.Printf("p90            : %dms\n", agg.P90())
			fmt.Printf("p95            : %dms\n", agg.P95())
			fmt.Printf("p99            : %dms\n", agg.P99())
			fmt.Printf("p999           : %dms\n", agg.P999())
			fmt.Printf("Max            : %dms\n", agg.Max())
			fmt.Printf("-----------------------------------\n")
			fmt.Printf("Errors         : %d\n", agg.ErrorCount())
			fmt.Printf("Error Rate     : %.2f%%\n", agg.ErrorRate())
			fmt.Printf("===================================\n")
		}
	},
}

func init() {
	runCmd.Flags().StringVar(&url,      "url",      "",    "Target URL")
	runCmd.Flags().IntVar(&vus,         "vus",      10,    "Virtual users")
	runCmd.Flags().StringVar(&duration, "duration", "30s", "Test duration")
	runCmd.Flags().IntVar(&rps,         "rps",      0,     "Max requests per second (0 = unlimited)")
	runCmd.Flags().StringVar(&output, "output", "text", "Output format: text or json")
	runCmd.Flags().StringVar(&flagScenarioFile, "scenario", "", "Path to YAML scenario file")
	runCmd.Flags().StringVar(&method, "method", "GET", "HTTP method (GET, POST, PUT, DELETE)")
	runCmd.Flags().StringArrayVar(&headers, "header", []string{}, "HTTP headers (e.g. --header 'Authorization: Bearer token')")
	runCmd.Flags().StringVar(&timeout, "timeout", "10s", "Per-request timeout")

	rootCmd.AddCommand(runCmd)
}