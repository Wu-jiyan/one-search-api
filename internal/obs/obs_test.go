package obs

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsRender(t *testing.T) {
	m := NewMetrics()
	m.Inc("onesearch_requests_total", "endpoint", "search", "status", "200")
	m.Inc("onesearch_requests_total", "endpoint", "search", "status", "200")
	m.Set("onesearch_inflight", 3)
	m.ObserveDuration("onesearch_request_duration_seconds", 1500*time.Millisecond, "endpoint", "search")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`onesearch_requests_total{endpoint="search",status="200"} 2`,
		`onesearch_inflight 3`,
		`onesearch_request_duration_seconds_sum{endpoint="search"} 1.5`,
		`onesearch_request_duration_seconds_count{endpoint="search"} 1`,
		`# TYPE onesearch_requests_total counter`,
		`# TYPE onesearch_inflight gauge`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("content type = %q", ct)
	}
}

func TestMetricKeySortsLabels(t *testing.T) {
	got := metricKey("m", []string{"z", "1", "a", "2"})
	want := `m{a="2",z="1"}`
	if got != want {
		t.Errorf("metricKey = %q, want %q", got, want)
	}
	if got := metricKey("m", nil); got != "m" {
		t.Errorf("metricKey(nil) = %q", got)
	}
}

func TestNewLogger(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unknown"} {
		for _, format := range []string{"json", "text"} {
			if l := NewLogger(level, format); l == nil {
				t.Fatalf("NewLogger(%q, %q) = nil", level, format)
			}
		}
	}
}
