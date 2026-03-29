package scenario

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sudesh856/suddpanzer/internal/assertions"
)

type Thresholds struct {
	P99Ms        float64 `yaml:"p99_ms"`
	P95Ms        float64 `yaml:"p95_ms"`
	ErrorRatePct float64 `yaml:"error_rate_pct"`
	MinRPS       float64 `yaml:"min_rps"`
}

type ThresholdFailure struct {
	Message string
}

func (t Thresholds) Evaluate(p99ms, p95ms int64, errorRatePct, avgRPS float64) []ThresholdFailure {
	var failures []ThresholdFailure
	if t.P99Ms > 0 && float64(p99ms) > t.P99Ms {
		failures = append(failures, ThresholdFailure{
			Message: fmt.Sprintf("THRESHOLD FAILED: p99=%dms > %.0fms", p99ms, t.P99Ms),
		})
	}
	if t.P95Ms > 0 && float64(p95ms) > t.P95Ms {
		failures = append(failures, ThresholdFailure{
			Message: fmt.Sprintf("THRESHOLD FAILED: p95=%dms > %.0fms", p95ms, t.P95Ms),
		})
	}
	if t.ErrorRatePct > 0 && errorRatePct > t.ErrorRatePct {
		failures = append(failures, ThresholdFailure{
			Message: fmt.Sprintf("THRESHOLD FAILED: error_rate=%.2f%% > %.2f%%", errorRatePct, t.ErrorRatePct),
		})
	}
	if t.MinRPS > 0 && avgRPS < t.MinRPS {
		failures = append(failures, ThresholdFailure{
			Message: fmt.Sprintf("THRESHOLD FAILED: avg_rps=%.2f < %.2f (minimum)", avgRPS, t.MinRPS),
		})
	}
	return failures
}

func (t Thresholds) IsZero() bool {
	return t.P99Ms == 0 && t.P95Ms == 0 && t.ErrorRatePct == 0 && t.MinRPS == 0
}

type Scenario struct {
	Name       string     `yaml:"name"`
	Stages     []Stage    `yaml:"stages"`
	Endpoints  []Endpoint `yaml:"endpoints"`
	Thresholds Thresholds `yaml:"thresholds"`
}

type Stage struct {
	Duration       string        `yaml:"duration"`
	TargetVUs      int           `yaml:"target_vus"`
	ParsedDuration time.Duration `yaml:"-"`
}

type Endpoint struct {
	Name           string            `yaml:"name"`
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method"`
	Headers        map[string]string `yaml:"headers"`
	Body           string            `yaml:"body"`
	Weight         int               `yaml:"weight"`
	ExpectedStatus int               `yaml:"expected_status"`
	Extract        map[string]string `yaml:"extract"`
	DependsOn      string            `yaml:"depends_on"`
	BasicAuth      string            `yaml:"basic_auth"`
	Script         string            `yaml:"script"`

	// gRPC
	GRPCTarget   string `yaml:"grpc_target"`
	GRPCMethod   string `yaml:"grpc_method"`
	GRPCPayload  string `yaml:"grpc_payload"`
	GRPCInsecure bool   `yaml:"grpc_insecure"`

	// WebSocket
	WSUrl               string        `yaml:"ws_url"`
	WSPayload           string        `yaml:"ws_payload"`
	WSReadTimeout       string        `yaml:"ws_read_timeout"`
	ParsedWSReadTimeout time.Duration `yaml:"-"`

	// Raw TCP
	TCPTarget            string        `yaml:"tcp_target"`
	TCPPayload           string        `yaml:"tcp_payload"`
	TCPReadBytes         int           `yaml:"tcp_read_bytes"`
	TCPReadTimeout       string        `yaml:"tcp_read_timeout"`
	ParsedTCPReadTimeout time.Duration `yaml:"-"`

	// Response assertions
	Assertions []assertions.Assertion `yaml:"assertions"`
}

func (e Endpoint) IsGRPC() bool { return e.GRPCTarget != "" }
func (e Endpoint) IsWS() bool   { return e.WSUrl != "" }
func (e Endpoint) IsTCP() bool  { return e.TCPTarget != "" }

func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Cannot read scenario file %q: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("Invalid YAML in %q: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Scenario) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("Scenario is missing a 'name' field.")
	}
	if len(s.Stages) == 0 {
		return fmt.Errorf("Scenario %q has no stages", s.Name)
	}
	for i, stage := range s.Stages {
		d, err := time.ParseDuration(stage.Duration)
		if err != nil {
			return fmt.Errorf("Stage[%d] invalid duration %q: %w", i, stage.Duration, err)
		}
		s.Stages[i].ParsedDuration = d
	}
	if len(s.Endpoints) == 0 {
		return fmt.Errorf("Scenario %q has no endpoints.", s.Name)
	}
	for i, ep := range s.Endpoints {
		if ep.Weight <= 0 {
			return fmt.Errorf("Endpoint[%d] %q: weight must be > 0", i, ep.Name)
		}
		if ep.IsGRPC() {
			if ep.GRPCMethod == "" {
				return fmt.Errorf("Endpoint[%d] %q: grpc_method is required when grpc_target is set", i, ep.Name)
			}
			continue
		}
		if ep.IsWS() {
			if ep.WSReadTimeout != "" {
				d, err := time.ParseDuration(ep.WSReadTimeout)
				if err != nil {
					return fmt.Errorf("Endpoint[%d] %q: invalid ws_read_timeout %q: %w", i, ep.Name, ep.WSReadTimeout, err)
				}
				s.Endpoints[i].ParsedWSReadTimeout = d
			}
			continue
		}
		if ep.IsTCP() {
			if ep.TCPReadTimeout != "" {
				d, err := time.ParseDuration(ep.TCPReadTimeout)
				if err != nil {
					return fmt.Errorf("Endpoint[%d] %q: invalid tcp_read_timeout %q: %w", i, ep.Name, ep.TCPReadTimeout, err)
				}
				s.Endpoints[i].ParsedTCPReadTimeout = d
			}
			continue
		}
		if ep.URL == "" {
			return fmt.Errorf("Endpoint [%d] missing url.", i)
		}
		if ep.Method == "" {
			s.Endpoints[i].Method = "GET"
		}
	}
	return nil
}