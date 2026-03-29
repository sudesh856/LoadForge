package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/sudesh856/suddpanzer/internal/assertions"
	"github.com/sudesh856/suddpanzer/internal/dnscache"
	"github.com/sudesh856/suddpanzer/internal/scripting"
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
	ScriptPool     *scripting.ScriptPool
	LuaScriptPool  *scripting.LuaScriptPool
	Assertions     []assertions.Assertion
	CookieJar      http.CookieJar
}

type Result struct {
	Latency           time.Duration
	StatusCode        int
	Err               error
	Bytes             int64
	Body              []byte
	EndpointName      string
	AssertionFailures []assertions.Failure
}

var defaultResolver = dnscache.Default()

// SetResolver replaces the DNS resolver used by all workers.
// Call once before starting the pool.
func SetResolver(r *dnscache.Resolver) {
	defaultResolver = r
}

func buildTransport(r *dnscache.Resolver) *http.Transport {
	return &http.Transport{
		DialContext:           r.DialContext,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     false,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   1000,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func NewCookieJar() (http.CookieJar, error) {
	return cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
}

func NewSessionJars(n int) ([]http.CookieJar, error) {
	jars := make([]http.CookieJar, n)
	for i := range jars {
		jar, err := NewCookieJar()
		if err != nil {
			return nil, err
		}
		jars[i] = jar
	}
	return jars, nil
}

func RunWorker(ctx context.Context, jobs <-chan Job, results chan<- Result) {
	transport := buildTransport(defaultResolver)
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}

	sessionClients := make(map[http.CookieJar]*http.Client)
	jsEngines := make(map[*scripting.ScriptPool]*scripting.Engine)
	luaEngines := make(map[*scripting.LuaScriptPool]*scripting.LuaEngine)

	defer func() {
		for _, eng := range luaEngines {
			eng.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}

			activeClient := client
			if job.CookieJar != nil {
				sc, exists := sessionClients[job.CookieJar]
				if !exists {
					sc = &http.Client{Timeout: 10 * time.Second, Transport: transport, Jar: job.CookieJar}
					sessionClients[job.CookieJar] = sc
				}
				activeClient = sc
			}

			if job.ScriptPool != nil {
				eng, exists := jsEngines[job.ScriptPool]
				if !exists {
					var err error
					eng, err = job.ScriptPool.Clone()
					if err != nil {
						results <- Result{Err: fmt.Errorf("js engine init: %w", err), EndpointName: job.Name}
						continue
					}
					jsEngines[job.ScriptPool] = eng
				}
				override, err := eng.Call()
				if err != nil {
					results <- Result{Err: fmt.Errorf("js call: %w", err), EndpointName: job.Name}
					continue
				}
				applyOverride(&job, override)
			}

			if job.LuaScriptPool != nil {
				eng, exists := luaEngines[job.LuaScriptPool]
				if !exists {
					var err error
					eng, err = job.LuaScriptPool.Clone()
					if err != nil {
						results <- Result{Err: fmt.Errorf("lua engine init: %w", err), EndpointName: job.Name}
						continue
					}
					luaEngines[job.LuaScriptPool] = eng
				}
				override, err := eng.Call()
				if err != nil {
					results <- Result{Err: fmt.Errorf("lua call: %w", err), EndpointName: job.Name}
					continue
				}
				applyOverride(&job, override)
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

			reqCtx := ctx
			var cancelReq context.CancelFunc
			if job.Timeout > 0 {
				reqCtx, cancelReq = context.WithTimeout(ctx, job.Timeout)
			}

			req, err := http.NewRequestWithContext(reqCtx, method, job.URL, bodyReader)
			if err != nil {
				if cancelReq != nil {
					cancelReq()
				}
				results <- Result{Latency: time.Since(start), Err: err, EndpointName: job.Name}
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

			if job.CookieJar != nil {
				reqURL, _ := url.Parse(job.URL)
				for _, cookie := range job.CookieJar.Cookies(reqURL) {
					req.AddCookie(cookie)
				}
			}

			resp, err := activeClient.Do(req)
			if cancelReq != nil {
				cancelReq()
			}
			latency := time.Since(start)

			if err != nil {
				results <- Result{Latency: latency, Err: err, EndpointName: job.Name}
				continue
			}

			if job.CookieJar != nil {
				reqURL, _ := url.Parse(job.URL)
				job.CookieJar.SetCookies(reqURL, resp.Cookies())
			}

			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var resultErr error
			if job.ExpectedStatus != 0 && resp.StatusCode != job.ExpectedStatus {
				resultErr = fmt.Errorf("expected status %d got %d", job.ExpectedStatus, resp.StatusCode)
			}

			var assertFailures []assertions.Failure
			if len(job.Assertions) > 0 {
				assertFailures = assertions.Run(job.Name, job.Assertions, resp, bodyBytes)
				if len(assertFailures) > 0 && resultErr == nil {
					resultErr = fmt.Errorf("assertion failed: %s", assertFailures[0].Message)
				}
			}

			results <- Result{
				Latency:           latency,
				StatusCode:        resp.StatusCode,
				Bytes:             int64(len(bodyBytes)),
				Body:              bodyBytes,
				EndpointName:      job.Name,
				Err:               resultErr,
				AssertionFailures: assertFailures,
			}
		}
	}
}

func applyOverride(job *Job, o *scripting.RequestOverride) {
	if o.Method != "" {
		job.Method = o.Method
	}
	if o.Body != "" {
		job.Body = o.Body
	}
	for k, v := range o.Headers {
		if job.Headers == nil {
			job.Headers = make(map[string]string)
		}
		job.Headers[k] = v
	}
}
