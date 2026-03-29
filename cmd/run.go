package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sudesh856/suddpanzer/internal/dnscache"
	"github.com/sudesh856/suddpanzer/internal/grpcworker"
	"github.com/sudesh856/suddpanzer/internal/scripting"
	"github.com/sudesh856/suddpanzer/internal/tcpworker"
	"github.com/sudesh856/suddpanzer/internal/wsworker"

	"github.com/spf13/cobra"
	"github.com/sudesh856/suddpanzer/internal/controller"
	"github.com/sudesh856/suddpanzer/internal/dashboard"
	"github.com/sudesh856/suddpanzer/internal/metrics"
	"github.com/sudesh856/suddpanzer/internal/output"
	"github.com/sudesh856/suddpanzer/internal/pool"
	promserver "github.com/sudesh856/suddpanzer/internal/prometheus"
	"github.com/sudesh856/suddpanzer/internal/ramp"
	"github.com/sudesh856/suddpanzer/internal/report"
	"github.com/sudesh856/suddpanzer/internal/reporter"
	"github.com/sudesh856/suddpanzer/internal/scenario"
	"github.com/sudesh856/suddpanzer/internal/store"
	"github.com/sudesh856/suddpanzer/internal/worker"
	"golang.org/x/time/rate"
)

var url string
var vus int
var duration string
var rps int
var outputFormat string // renamed from "output" to avoid collision with package name
var flagScenarioFile string
var method string
var headers []string
var timeout string
var basicAuth string
var expectedStatus int
var webFlag bool
var runName string

var metricsAddr string
var outputFile string
var distributedFile string // Phase 5

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a load test",
	Run: func(cmd *cobra.Command, args []string) {

		// ── Phase 5: DISTRIBUTED MODE ──────────────────────────────────────
		if distributedFile != "" {
			runDistributed()
			return
		}

		st, stErr := store.New("./blast.db")
		if stErr != nil {
			fmt.Println("warning: could not open store:", stErr)
		}
		if st != nil {
			defer st.Close()
		}

		var promSnap *promserver.Snapshot
		if metricsAddr != "" {
			srv := promserver.New(metricsAddr)
			srv.Start()
			defer srv.Stop()
			promSnap = srv.Snap()
			fmt.Fprintf(os.Stderr, "metrics: http://%s/metrics\n", metricsAddr)
		}

		if flagScenarioFile != "" {
			s, err := scenario.LoadScenario(flagScenarioFile)
			if err != nil {
				fmt.Println("scenario error:", err)
				return
			}

			// ── CHANGE 2: Wire DNS resolver after loading scenario ──────────
			// set up DNS resolver (caching + optional custom servers/overrides)
			dnsResolver, err := dnscache.New(dnscache.Config{
				CacheTTL:  s.DNS.CacheTTL,
				Servers:   s.DNS.Servers,
				Overrides: s.DNS.Overrides,
			})
			if err != nil {
				fmt.Println("dns config error:", err)
				return
			}
			worker.SetResolver(dnsResolver)

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

			// - web dashboard
			var dash *dashboard.Server
			if webFlag {
				dashSt, err := store.New("runs.db")
				if err != nil {
					log.Fatal(err)
				}
				dash = dashboard.New(cancelSignal, dashSt)
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

			// Create HTTP, gRPC, WebSocket, and TCP pools
			p := pool.New(1000)
			p.Start(ctx, maxVUs)

			gp := grpcworker.NewPool(1000)
			gp.Start(ctx, maxVUs)

			wp := wsworker.NewPool(1000)
			wp.Start(ctx, maxVUs)

			tp := tcpworker.NewPool(1000)
			tp.Start(ctx, maxVUs)

			agg := metrics.New()
			// merge all result channels into one aggregator
			mergedResults := mergeResults(p.Results(), gp.Results(), wp.Results(), tp.Results())
			agg.Start(mergedResults)
			start := time.Now()

			// - broadcast to dashboard every 500ms -
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

			if promSnap != nil {
				go func() {
					tick := time.NewTicker(500 * time.Millisecond)
					defer tick.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-tick.C:
							elapsed := time.Since(start)
							promSnap.SetRPS(agg.RPS(elapsed))
							promSnap.LatencyP99Ms.Store(agg.P99())
							promSnap.LatencyP95Ms.Store(agg.P95())
							promSnap.LatencyP50Ms.Store(agg.P50())
							promSnap.ErrorTotal.Store(agg.ErrorCount())
							promSnap.RequestsTotal.Store(agg.TotalRequests())
							promSnap.VUsActive.Store(int64(maxVUs))
						}
					}
				}()
			}

			var wg sync.WaitGroup
			wg.Add(1)
			chainStore := scenario.NewChainStore()
			go func() {
				for result := range mergedResults {
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

			sharedJar, err := worker.NewCookieJar()
			if err != nil {
				log.Fatal("could not create cookie jar:", err)
			}

			scriptPools := make(map[string]*scripting.ScriptPool)
			luaScriptPools := make(map[string]*scripting.LuaScriptPool)
			for _, ep := range s.Endpoints {
				if ep.Script != "" {
					if strings.HasSuffix(ep.Script, ".lua") {
						lsp, err := scripting.NewLuaScriptPoolFromFile(ep.Script)
						if err != nil {
							log.Printf("warning: lua script load failed for %q: %v", ep.Name, err)
						} else {
							luaScriptPools[ep.Name] = lsp
						}
					} else {
						sp, err := scripting.NewScriptPoolFromFile(ep.Script)
						if err != nil {
							log.Printf("warning: js script load failed for %q: %v", ep.Name, err)
						} else {
							scriptPools[ep.Name] = sp
						}
					}
				}
			}

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

						// Route gRPC, WebSocket, and TCP endpoints
						if ep.IsGRPC() {
							payload, err := grpcworker.BuildPayload(ep.GRPCPayload)
							if err != nil {
								log.Printf("grpc payload error for %q: %v", ep.Name, err)
								<-ctrl.Semaphore
								continue
							}
							gp.Submit(grpcworker.GRPCJob{
								EndpointName: ep.Name,
								Target:       ep.GRPCTarget,
								Method:       ep.GRPCMethod,
								Payload:      payload,
								Headers:      ep.Headers,
								Insecure:     ep.GRPCInsecure,
								Timeout:      10 * time.Second,
							})
						} else if ep.IsWS() {
							wp.Submit(wsworker.WSJob{
								EndpointName: ep.Name,
								URL:          ep.WSUrl,
								Payload:      scenario.ReplaceVars(ep.WSPayload, vars),
								Headers:      ep.Headers,
								ReadTimeout:  ep.ParsedWSReadTimeout,
							})
						} else if ep.IsTCP() {
							tp.Submit(tcpworker.TCPJob{
								EndpointName: ep.Name,
								Target:       ep.TCPTarget,
								Payload:      scenario.ReplaceVars(ep.TCPPayload, vars),
								ReadBytes:    ep.TCPReadBytes,
								ReadTimeout:  ep.ParsedTCPReadTimeout,
							})
						} else {
							// Assign jar when building the HTTP job
							var jar http.CookieJar
							if ep.CookieSession {
								jar = sharedJar
							}
							job := worker.Job{
								Name:           ep.Name,
								URL:            epURL,
								Method:         ep.Method,
								Body:           body,
								ExpectedStatus: ep.ExpectedStatus,
								Headers:        ep.Headers,
								BasicAuth:      ep.BasicAuth,
								Assertions:     ep.Assertions,
								CookieJar:      jar,
							}
							if ep.Script != "" {
								if strings.HasSuffix(ep.Script, ".lua") {
									// Lua script
									if lsp, ok := luaScriptPools[ep.Name]; ok {
										job.LuaScriptPool = lsp
									} else {
										lsp, err := scripting.NewLuaScriptPoolFromFile(ep.Script)
										if err != nil {
											log.Printf("warning: lua script load failed for %q: %v", ep.Name, err)
										} else {
											job.LuaScriptPool = lsp
											luaScriptPools[ep.Name] = lsp
										}
									}
								} else {
									// JS script (default)
									if sp, ok := scriptPools[ep.Name]; ok {
										job.ScriptPool = sp
									} else {
										sp, err := scripting.NewScriptPoolFromFile(ep.Script)
										if err != nil {
											log.Printf("warning: js script load failed for %q: %v", ep.Name, err)
										} else {
											job.ScriptPool = sp
											scriptPools[ep.Name] = sp
										}
									}
								}
							}
							p.Submit(job)
						}
						<-ctrl.Semaphore
					}
				}
			}()

			<-ctx.Done()
			wg.Wait()
			time.Sleep(100 * time.Millisecond)

			elapsed := time.Since(start)
			scenarioURL := ""
			if len(s.Endpoints) > 0 {
				scenarioURL = s.Endpoints[0].URL
			}
			sum := report.Summary{
				URL:            scenarioURL,
				ScenarioName:   s.Name,
				DurationSecs:   elapsed.Seconds(),
				TotalRequests:  agg.TotalRequests(),
				AvgRPS:         agg.RPS(elapsed),
				P50:            agg.P50(),
				P75:            agg.P75(),
				P90:            agg.P90(),
				P95:            agg.P95(),
				P99:            agg.P99(),
				P999:           agg.P999(),
				Max:            agg.Max(),
				Errors:         agg.ErrorCount(),
				ErrorRate:      agg.ErrorRate(),
				ErrorBreakdown: agg.ErrorBreakdown(),
			}

			// - save to store -----------------------
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

			// - generate HTML report -------------------
			if reportFile, err := report.Generate(sum); err == nil {
				fmt.Printf("report: %s\n", reportFile)
			}

			// - Phase 4: threshold evaluation --------------
			var thresholdFailures []string
			if !s.Thresholds.IsZero() {
				failures := s.Thresholds.Evaluate(sum.P99, sum.P95, sum.ErrorRate, sum.AvgRPS)
				for _, f := range failures {
					thresholdFailures = append(thresholdFailures, f.Message)
					fmt.Fprintln(os.Stderr, f.Message)
				}
			}

			// - Phase 4: write output in requested format ---------
			writeOutput(outputFormat, outputFile, sum, thresholdFailures)

			// - Phase 4: exit 1 if any threshold failed ----------
			if len(thresholdFailures) > 0 {
				os.Exit(1)
			}
			return
		}

		// - SINGLE URL MODE ------------------------
		dur, err := time.ParseDuration(duration)
		if err != nil {
			fmt.Println("invalid duration:", err)
			return
		}

		ctx, cancelSignal := signal.NotifyContext(context.Background(), os.Interrupt)
		ctx, cancelTimeout := context.WithTimeout(ctx, dur)
		defer cancelTimeout()
		defer cancelSignal()

		// - web dashboard -------------------------
		var dash *dashboard.Server
		if webFlag {
			dashSt, err := store.New("runs.db")
			if err != nil {
				log.Fatal(err)
			}
			dash = dashboard.New(cancelSignal, dashSt)
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

		// - broadcast to dashboard every 500ms -
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

		if promSnap != nil {
			go func() {
				tick := time.NewTicker(500 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						elapsed := time.Since(start)
						promSnap.SetRPS(agg.RPS(elapsed))
						promSnap.LatencyP99Ms.Store(agg.P99())
						promSnap.LatencyP95Ms.Store(agg.P95())
						promSnap.LatencyP50Ms.Store(agg.P50())
						promSnap.ErrorTotal.Store(agg.ErrorCount())
						promSnap.RequestsTotal.Store(agg.TotalRequests())
						promSnap.VUsActive.Store(int64(vus))
					}
				}
			}()
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
			URL:            url,
			DurationSecs:   elapsed.Seconds(),
			TotalRequests:  agg.TotalRequests(),
			AvgRPS:         agg.RPS(elapsed),
			P50:            agg.P50(),
			P75:            agg.P75(),
			P90:            agg.P90(),
			P95:            agg.P95(),
			P99:            agg.P99(),
			P999:           agg.P999(),
			Max:            agg.Max(),
			Errors:         agg.ErrorCount(),
			ErrorRate:      agg.ErrorRate(),
			ErrorBreakdown: agg.ErrorBreakdown(),
		}

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

		// - generate HTML report ---------------------
		if reportFile, err := report.Generate(sum); err == nil {
			fmt.Printf("report: %s\n", reportFile)
		}

		writeOutput(outputFormat, outputFile, sum, nil)
	},
}

// runDistributed hands off to the controller package.
func runDistributed() {
	if flagScenarioFile == "" {
		fmt.Println("error: --scenario is required with --distributed")
		os.Exit(1)
	}

	af, err := controller.LoadAgentsFile(distributedFile)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	scenarioBytes, err := os.ReadFile(flagScenarioFile)
	if err != nil {
		fmt.Println("error reading scenario:", err)
		os.Exit(1)
	}

	fmt.Printf("[controller] distributed run across %d agent(s)\n", len(af.Agents))
	for _, a := range af.Agents {
		fmt.Printf("  → %s @ %s (region: %s)\n", a.ID, a.Address, a.Region)
	}
	fmt.Println()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	results, err := controller.Run(ctx, af, string(scenarioBytes))
	if err != nil {
		fmt.Println("distributed run error:", err)
		os.Exit(1)
	}

	if outputFormat == "json" {
		data, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(data))
	}
}

func writeOutput(format, filePath string, sum report.Summary, thresholdFailures []string) {
	// Decide where to write: file or stdout.
	w := os.Stdout
	if filePath != "" {
		f, err := os.Create(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create output file %q: %v\n", filePath, err)
		} else {
			defer f.Close()
			w = f
		}
	}

	var err error
	switch format {
	case "json":
		err = output.WriteJSON(w, sum)
	case "csv":
		err = output.WriteCSV(w, sum)
	case "junit":
		err = output.WriteJUnit(w, sum, thresholdFailures)
	default: // "text"
		if sum.ScenarioName != "" {
			printScenarioSummary(sum)
		} else {
			printSingleSummary(sum, vus)
		}

		if len(thresholdFailures) > 0 {
			fmt.Println()
			for _, msg := range thresholdFailures {
				fmt.Println(msg)
			}
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: output error: %v\n", err)
	}
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
	if len(sum.ErrorBreakdown) > 0 {
		for msg, count := range sum.ErrorBreakdown {
			fmt.Printf("  %dx %s\n", count, msg)
		}
	}
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
	if len(sum.ErrorBreakdown) > 0 {
		for msg, count := range sum.ErrorBreakdown {
			fmt.Printf("  %dx %s\n", count, msg)
		}
	}
	fmt.Printf("=================================\n")
}

// mergeResults fans multiple result channels into one.
// This lets the metrics aggregator consume HTTP + gRPC + WebSocket + TCP results together.
func mergeResults(channels ...<-chan worker.Result) <-chan worker.Result {
	merged := make(chan worker.Result, 2000)
	var wg sync.WaitGroup
	for _, ch := range channels {
		wg.Add(1)
		go func(c <-chan worker.Result) {
			defer wg.Done()
			for r := range c {
				merged <- r
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(merged)
	}()
	return merged
}

func init() {
	runCmd.Flags().StringVar(&url, "url", "", "Target URL")
	runCmd.Flags().IntVar(&vus, "vus", 10, "Virtual users")
	runCmd.Flags().StringVar(&duration, "duration", "30s", "Test duration")
	runCmd.Flags().IntVar(&rps, "rps", 0, "Max requests per second (0 = unlimited)")
	runCmd.Flags().StringVar(&outputFormat, "output", "text", "Output format: text | json | csv | junit")
	runCmd.Flags().StringVar(&flagScenarioFile, "scenario", "", "Path to YAML scenario file")
	runCmd.Flags().StringVar(&method, "method", "GET", "HTTP method (GET, POST, PUT, DELETE)")
	runCmd.Flags().StringArrayVar(&headers, "header", []string{}, "HTTP headers (e.g. --header 'Authorization: Bearer token')")
	runCmd.Flags().StringVar(&timeout, "timeout", "10s", "Per-request timeout")
	runCmd.Flags().StringVar(&basicAuth, "auth", "", "Basic auth in user:password format")
	runCmd.Flags().IntVar(&expectedStatus, "expected-status", 0, "Expected HTTP status code (0 = any)")
	runCmd.Flags().BoolVar(&webFlag, "web", false, "Enable live web dashboard on :7070")
	runCmd.Flags().StringVar(&runName, "name", "", "Name for this run (saved to history)")

	// Phase 4 flags
	runCmd.Flags().StringVar(&metricsAddr, "metrics-addr", "", "Expose Prometheus /metrics on this address during run (e.g. :9090)")
	runCmd.Flags().StringVar(&outputFile, "output-file", "", "Write output to file (default: stdout)")

	// Phase 5 flag
	runCmd.Flags().StringVar(&distributedFile, "distributed", "", "agents.yaml for distributed mode")

	rootCmd.AddCommand(runCmd)
}
