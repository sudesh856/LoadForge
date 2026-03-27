package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"encoding/json"

	"github.com/spf13/cobra"
	"github.com/sudesh856/LoadForge/internal/dashboard"
	"github.com/sudesh856/LoadForge/internal/metrics"
	"github.com/sudesh856/LoadForge/internal/pool"
	"github.com/sudesh856/LoadForge/internal/ramp"
	"github.com/sudesh856/LoadForge/internal/report"
	"github.com/sudesh856/LoadForge/internal/reporter"
	"github.com/sudesh856/LoadForge/internal/scenario"
	"github.com/sudesh856/LoadForge/internal/store"
	"github.com/sudesh856/LoadForge/internal/worker"
	"golang.org/x/time/rate"
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
var basicAuth string
var expectedStatus int
var webFlag bool
var runName string

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a load test",
	Run: func(cmd *cobra.Command, args []string) {

		// ── open store (always, so we save every run) ──────────────────────
		st, stErr := store.New("./blast.db")
		if stErr != nil {
			fmt.Println("warning: could not open store:", stErr)
		}
		if st != nil {
			defer st.Close()
		}

		// ── SCENARIO MODE ──────────────────────────────────────────────────
		if flagScenarioFile != "" {
			s, err := scenario.LoadScenario(flagScenarioFile)
			if err != nil {
				fmt.Println("scenario error:", err)
				return
			}

			rampStages := make([]ramp.Stage, len(s.Stages))
			for i, st := range s.Stages {
				rampStages[i] = ramp.Stage{
					Duration:  st.ParsedDuration,
					TargetVUs: st.TargetVUs,
				}
			}

			totalDur := time.Duration(0)
			for _, st := range s.Stages {
				totalDur += st.ParsedDuration
			}

			ctx, cancelSignal := signal.NotifyContext(context.Background(), os.Interrupt)
			ctx, cancelTimeout := context.WithTimeout(ctx, totalDur)
			defer cancelTimeout()
			defer cancelSignal()

			// ── web dashboard ──
			// ── web dashboard ──
var dash *dashboard.Server
if webFlag {
    st, err := store.New("runs.db")
    if err != nil {
        log.Fatal(err)
    }

    dash = dashboard.New(cancelSignal, st)
    dash.Start(":7070")
    fmt.Println("dashboard: http://localhost:7070")
}

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

			// ── broadcast to dashboard every 500ms ──
			if dash != nil {
				dash.StartBroadcasting(func() dashboard.MetricsSnapshot {
					elapsed := time.Since(start)
					return dashboard.MetricsSnapshot{
						Timestamp: time.Now().Unix(),
						RPS:       agg.RPS(elapsed),
						P50:       agg.P50(),
						P95:       agg.P95(),
						P99:       agg.P99(),
						ErrorRate: agg.ErrorRate(),
						TotalReqs: agg.TotalRequests(),
					}
				}, ctx)
			}

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
						epURL := scenario.ReplaceVars(ep.URL, vars)
						body := scenario.ReplaceVars(ep.Body, vars)
						p.Submit(worker.Job{
							Name:           ep.Name,
							URL:            epURL,
							Method:         ep.Method,
							Body:           body,
							ExpectedStatus: ep.ExpectedStatus,
							Headers:        ep.Headers,
							BasicAuth:      ep.BasicAuth,
						})
						<-ctrl.Semaphore
					}
				}
			}()

			<-ctx.Done()
			wg.Wait()
			time.Sleep(100 * time.Millisecond)

			elapsed := time.Since(start)
			sum := report.Summary{
				ScenarioName:  s.Name,
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

			// ── save to store ──
			if st != nil {
				name := runName
				if name == "" {
					name = s.Name
				}
				configData := map[string]interface{}{"scenario": flagScenarioFile}
				if _, err := st.SaveRun(name, configData, sum); err != nil {
					fmt.Println("warning: could not save run:", err)
				}
			}

			// ── generate HTML report ──
			if reportFile, err := report.Generate(sum); err == nil {
				fmt.Printf("report: %s\n", reportFile)
			}

			if output == "json" {
				data, _ := json.MarshalIndent(sum, "", "  ")
				fmt.Println("\n" + string(data))
			} else {
				printScenarioSummary(sum)
			}
			return
		}

		// ── SINGLE URL MODE ────────────────────────────────────────────────
		dur, err := time.ParseDuration(duration)
		if err != nil {
			fmt.Println("invalid duration:", err)
			return
		}

		ctx, cancelSignal := signal.NotifyContext(context.Background(), os.Interrupt)
		ctx, cancelTimeout := context.WithTimeout(ctx, dur)
		defer cancelTimeout()
		defer cancelSignal()

		// ── web dashboard ──
		// ── web dashboard ──
var dash *dashboard.Server
if webFlag {
    st, err := store.New("runs.db")
    if err != nil {
        log.Fatal(err)
    }

    dash = dashboard.New(cancelSignal, st)
    dash.Start(":7070")
    fmt.Println("dashboard: http://localhost:7070")
}

		var limiter *rate.Limiter
		if rps > 0 {
			limiter = rate.NewLimiter(rate.Limit(rps), rps)
		}

		p := pool.New(1000)
		p.Start(ctx, vus)

		agg := metrics.New()
		agg.Start(p.Results())

		start := time.Now()

		// ── broadcast to dashboard every 500ms ──
		if dash != nil {
			dash.StartBroadcasting(func() dashboard.MetricsSnapshot {
				elapsed := time.Since(start)
				return dashboard.MetricsSnapshot{
					Timestamp: time.Now().Unix(),
					RPS:       agg.RPS(elapsed),
					P50:       agg.P50(),
					P95:       agg.P95(),
					P99:       agg.P99(),
					ErrorRate: agg.ErrorRate(),
					TotalReqs: agg.TotalRequests(),
				}
			}, ctx)
		}

		headerMap := map[string]string{}
		for _, h := range headers {
			parts := strings.SplitN(h, ":", 2)
			if len(parts) == 2 {
				headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}

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
						URL:            url,
						Method:         method,
						Headers:        headerMap,
						Timeout:        reqTimeout,
						BasicAuth:      basicAuth,
						ExpectedStatus: expectedStatus,
					})
				}
			}
		}()

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

		<-ctx.Done()
		wg.Wait()
		time.Sleep(100 * time.Millisecond)

		elapsed := time.Since(start)
		sum := report.Summary{
			URL:           url,
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

		// ── save to store ──
		if st != nil {
			name := runName
			if name == "" {
				name = url
			}
			configData := map[string]interface{}{
				"url": url, "vus": vus, "duration": duration,
			}
			if _, err := st.SaveRun(name, configData, sum); err != nil {
				fmt.Println("warning: could not save run:", err)
			}
		}

		// ── generate HTML report ──
		if reportFile, err := report.Generate(sum); err == nil {
			fmt.Printf("report: %s\n", reportFile)
		}

		if output == "json" {
			data, _ := json.MarshalIndent(sum, "", "  ")
			fmt.Println("\n" + string(data))
		} else {
			printSingleSummary(sum, vus)
		}
	},
}

func printScenarioSummary(sum report.Summary) {
	fmt.Printf("\n\n----- SUDD SCENARIO SUMMARY -----\n")
	fmt.Printf("Scenario       : %s\n", sum.ScenarioName)
	fmt.Printf("Duration       : %.1fs\n", sum.DurationSecs)
	fmt.Printf("-----------------------------------\n")
	fmt.Printf("Total Requests : %d\n", sum.TotalRequests)
	fmt.Printf("Avg RPS        : %.2f\n", sum.AvgRPS)
	fmt.Printf("-----------------------------------\n")
	fmt.Printf("p50            : %dms\n", sum.P50)
	fmt.Printf("p75            : %dms\n", sum.P75)
	fmt.Printf("p90            : %dms\n", sum.P90)
	fmt.Printf("p95            : %dms\n", sum.P95)
	fmt.Printf("p99            : %dms\n", sum.P99)
	fmt.Printf("p999           : %dms\n", sum.P999)
	fmt.Printf("Max            : %dms\n", sum.Max)
	fmt.Printf("-----------------------------------\n")
	fmt.Printf("Errors         : %d\n", sum.Errors)
	fmt.Printf("Error Rate     : %.2f%%\n", sum.ErrorRate)
	fmt.Printf("=================================\n")
}

func printSingleSummary(sum report.Summary, vus int) {
	fmt.Printf("\n\n----- SUDD LOAD TEST SUMMARY -----\n")
	fmt.Printf("URL            : %s\n", sum.URL)
	fmt.Printf("VUs            : %d\n", vus)
	fmt.Printf("Duration       : %.1fs\n", sum.DurationSecs)
	fmt.Printf("-----------------------------------\n")
	fmt.Printf("Total Requests : %d\n", sum.TotalRequests)
	fmt.Printf("Avg RPS        : %.2f\n", sum.AvgRPS)
	fmt.Printf("-----------------------------------\n")
	fmt.Printf("p50            : %dms\n", sum.P50)
	fmt.Printf("p75            : %dms\n", sum.P75)
	fmt.Printf("p90            : %dms\n", sum.P90)
	fmt.Printf("p95            : %dms\n", sum.P95)
	fmt.Printf("p99            : %dms\n", sum.P99)
	fmt.Printf("p999           : %dms\n", sum.P999)
	fmt.Printf("Max            : %dms\n", sum.Max)
	fmt.Printf("-----------------------------------\n")
	fmt.Printf("Errors         : %d\n", sum.Errors)
	fmt.Printf("Error Rate     : %.2f%%\n", sum.ErrorRate)
	fmt.Printf("===================================\n")
}

func init() {
	runCmd.Flags().StringVar(&url,      "url",      "",    "Target URL")
	runCmd.Flags().IntVar(&vus,         "vus",      10,    "Virtual users")
	runCmd.Flags().StringVar(&duration, "duration", "30s", "Test duration")
	runCmd.Flags().IntVar(&rps,         "rps",      0,     "Max requests per second (0 = unlimited)")
	runCmd.Flags().StringVar(&output,   "output",   "text","Output format: text or json")
	runCmd.Flags().StringVar(&flagScenarioFile, "scenario", "", "Path to YAML scenario file")
	runCmd.Flags().StringVar(&method,   "method",   "GET", "HTTP method (GET, POST, PUT, DELETE)")
	runCmd.Flags().StringArrayVar(&headers, "header", []string{}, "HTTP headers (e.g. --header 'Authorization: Bearer token')")
	runCmd.Flags().StringVar(&timeout,  "timeout",  "10s", "Per-request timeout")
	runCmd.Flags().StringVar(&basicAuth,"auth",     "",    "Basic auth in user:password format")
	runCmd.Flags().IntVar(&expectedStatus, "expected-status", 0, "Expected HTTP status code (0 = any)")
	runCmd.Flags().BoolVar(&webFlag,    "web",      false, "Enable live web dashboard on :7070")
	runCmd.Flags().StringVar(&runName,  "name",     "",    "Name for this run (saved to history)")

	rootCmd.AddCommand(runCmd)
}