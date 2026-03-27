package report

import (
	"fmt"
	"os"
	"text/template"
	"time"
)

type Summary struct {
	URL           string  `json:"url"`
	ScenarioName  string  `json:"scenario_name"`
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

const htmlTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>blast — Run Report</title>
<script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/4.4.1/chart.umd.min.js"></script>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { background: #0f0f0f; color: #e0e0e0; font-family: monospace; padding: 32px; }
  h1 { color: #00ff88; margin-bottom: 8px; font-size: 1.6rem; }
  .meta { color: #666; font-size: 0.85rem; margin-bottom: 28px; }
  .stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; margin-bottom: 28px; }
  .stat { background: #1a1a1a; border: 1px solid #333; border-radius: 8px; padding: 16px; text-align: center; }
  .stat .value { font-size: 2rem; font-weight: bold; color: #00ff88; }
  .stat .label { font-size: 0.7rem; color: #888; margin-top: 6px; text-transform: uppercase; }
  .card { background: #1a1a1a; border: 1px solid #333; border-radius: 8px; padding: 20px; margin-bottom: 20px; }
  .card h2 { color: #888; font-size: 0.75rem; text-transform: uppercase; margin-bottom: 16px; }
  table { width: 100%; border-collapse: collapse; }
  td, th { padding: 8px 12px; text-align: left; border-bottom: 1px solid #222; font-size: 0.9rem; }
  th { color: #888; font-weight: normal; }
  td:first-child { color: #888; }
  td:last-child { color: #00ff88; }
  .chart-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; margin-bottom: 20px; }
</style>
</head>
<body>
<h1>blast — Run Report</h1>
<div class="meta">Generated: {{.GeneratedAt}}{{if .Summary.ScenarioName}} &nbsp;|&nbsp; Scenario: {{.Summary.ScenarioName}}{{end}}{{if .Summary.URL}} &nbsp;|&nbsp; URL: {{.Summary.URL}}{{end}}</div>

<div class="stats">
  <div class="stat"><div class="value">{{printf "%.1f" .Summary.AvgRPS}}</div><div class="label">Avg RPS</div></div>
  <div class="stat"><div class="value">{{.Summary.P99}}ms</div><div class="label">p99 Latency</div></div>
  <div class="stat"><div class="value">{{printf "%.2f" .Summary.ErrorRate}}%</div><div class="label">Error Rate</div></div>
  <div class="stat"><div class="value">{{.Summary.TotalRequests}}</div><div class="label">Total Requests</div></div>
</div>

<div class="chart-grid">
  <div class="card">
    <h2>Latency Percentiles (ms)</h2>
    <canvas id="latencyChart"></canvas>
  </div>
  <div class="card">
    <h2>Latency Distribution</h2>
    <canvas id="distChart"></canvas>
  </div>
</div>

<div class="card">
  <h2>Full Summary</h2>
  <table>
    <tr><td>Duration</td><td>{{printf "%.1f" .Summary.DurationSecs}}s</td></tr>
    <tr><td>Total Requests</td><td>{{.Summary.TotalRequests}}</td></tr>
    <tr><td>Avg RPS</td><td>{{printf "%.2f" .Summary.AvgRPS}}</td></tr>
    <tr><td>p50</td><td>{{.Summary.P50}}ms</td></tr>
    <tr><td>p75</td><td>{{.Summary.P75}}ms</td></tr>
    <tr><td>p90</td><td>{{.Summary.P90}}ms</td></tr>
    <tr><td>p95</td><td>{{.Summary.P95}}ms</td></tr>
    <tr><td>p99</td><td>{{.Summary.P99}}ms</td></tr>
    <tr><td>p999</td><td>{{.Summary.P999}}ms</td></tr>
    <tr><td>Max</td><td>{{.Summary.Max}}ms</td></tr>
    <tr><td>Errors</td><td>{{.Summary.Errors}}</td></tr>
    <tr><td>Error Rate</td><td>{{printf "%.2f" .Summary.ErrorRate}}%</td></tr>
  </table>
</div>

<script>
new Chart(document.getElementById('latencyChart'), {
  type: 'bar',
  data: {
    labels: ['p50','p75','p90','p95','p99','p999','Max'],
    datasets: [{
      label: 'ms',
      data: [{{.Summary.P50}},{{.Summary.P75}},{{.Summary.P90}},{{.Summary.P95}},{{.Summary.P99}},{{.Summary.P999}},{{.Summary.Max}}],
      backgroundColor: ['#00ff88','#00dd77','#ffaa00','#ff8800','#ff4444','#cc0000','#880000'],
    }]
  },
  options: { animation: false, plugins: { legend: { display: false } }, scales: { y: { beginAtZero: true } } }
});

new Chart(document.getElementById('distChart'), {
  type: 'doughnut',
  data: {
    labels: ['p50','p50–p95','p95–p99','p99+'],
    datasets: [{
      data: [
        {{.Summary.P50}},
        {{.Summary.P95}} - {{.Summary.P50}},
        {{.Summary.P99}} - {{.Summary.P95}},
        {{.Summary.Max}} - {{.Summary.P99}}
      ],
      backgroundColor: ['#00ff88','#ffaa00','#ff8800','#ff4444'],
    }]
  },
  options: { animation: false, plugins: { legend: { labels: { color: '#888' } } } }
});
</script>
</body>
</html>`

type templateData struct {
	Summary     Summary
	GeneratedAt string
}

func Generate(sum Summary) (string, error) {
	filename := fmt.Sprintf("report-%s.html", time.Now().Format("2006-01-02T15-04-05"))

	tmpl, err := template.New("report").Parse(htmlTmpl)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	f, err := os.Create(filename)
	if err != nil {
		return "", fmt.Errorf("cannot create report file: %w", err)
	}
	defer f.Close()

	data := templateData{
		Summary:     sum,
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
	}

	if err := tmpl.Execute(f, data); err != nil {
		return "", fmt.Errorf("template execute error: %w", err)
	}

	return filename, nil
}