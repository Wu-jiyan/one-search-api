// Package obs provides the logging and Prometheus-style metrics primitives.
package obs

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NewLogger builds a slog.Logger from level and format ("json"|"text").
func NewLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.ToLower(format) == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// Metrics is a tiny thread-safe metrics registry rendering Prometheus text.
type Metrics struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
	help     map[string]string
}

// NewMetrics creates an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{
		counters: map[string]float64{},
		gauges:   map[string]float64{},
		help:     map[string]string{},
	}
}

// Inc adds 1 to a counter.
func (m *Metrics) Inc(name string, labels ...string) { m.Add(name, 1, labels...) }

// Add adds v to a counter.
func (m *Metrics) Add(name string, v float64, labels ...string) {
	key := metricKey(name, labels)
	m.mu.Lock()
	m.counters[key] += v
	m.mu.Unlock()
}

// Set sets a gauge.
func (m *Metrics) Set(name string, v float64, labels ...string) {
	key := metricKey(name, labels)
	m.mu.Lock()
	m.gauges[key] = v
	m.mu.Unlock()
}

// ObserveDuration records a duration into a _sum/_count pair of counters.
func (m *Metrics) ObserveDuration(name string, d time.Duration, labels ...string) {
	secs := d.Seconds()
	m.Add(name+"_sum", secs, labels...)
	m.Add(name+"_count", 1, labels...)
}

// Handler serves the registry in Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.mu.Lock()
		counters := make(map[string]float64, len(m.counters))
		for k, v := range m.counters {
			counters[k] = v
		}
		gauges := make(map[string]float64, len(m.gauges))
		for k, v := range m.gauges {
			gauges[k] = v
		}
		m.mu.Unlock()

		writeGroup(w, counters, "counter")
		writeGroup(w, gauges, "gauge")
	})
}

func writeGroup(w io.Writer, values map[string]float64, typ string) {
	names := make(map[string][]string)
	for key := range values {
		name := key
		if idx := strings.IndexByte(key, '{'); idx >= 0 {
			name = key[:idx]
		}
		names[name] = append(names[name], key)
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		fmt.Fprintf(w, "# TYPE %s %s\n", name, typ)
		keys := names[name]
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(w, "%s %s\n", key, strconv.FormatFloat(values[key], 'g', -1, 64))
		}
	}
}

// metricKey renders name{k="v"} with labels sorted by key.
func metricKey(name string, labels []string) string {
	if len(labels) < 2 {
		return name
	}
	pairs := make([]string, 0, len(labels)/2)
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, labels[i]+"="+strconv.Quote(labels[i+1]))
	}
	sort.Strings(pairs)
	return name + "{" + strings.Join(pairs, ",") + "}"
}
