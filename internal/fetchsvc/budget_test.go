package fetchsvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/httpx"
	"github.com/Wu-jiyan/one-search-api/internal/obs"
)

// stubMeter records what the fetch service charges.
type stubMeter struct {
	mu        sync.Mutex
	reserved  int64
	committed int64
	calls     int
	fail      *httpx.APIError
}

func (m *stubMeter) ReserveFetch(maxBytes int64) (int64, *httpx.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return 0, m.fail
	}
	m.calls++
	m.reserved += maxBytes
	return maxBytes, nil
}

func (m *stubMeter) CommitFetch(reserved, actual int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.committed += actual
}

func (m *stubMeter) AcquireFetch() bool { return true }
func (m *stubMeter) ReleaseFetch()      {}

func (m *stubMeter) snapshot() (calls int, committed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.committed
}

func TestFetchChargesTransferredBytes(t *testing.T) {
	body := "<html><head><title>t</title></head><body><article>" +
		"<h1>h</h1><p>" + string(make([]byte, 0)) + "0123456789abcdefghijklmnopqrstuvwxyz0123456789" +
		"</p></article></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	svc := New(testConfig(t, 5*time.Second, srv.URL), obs.NewMetrics())
	meter := &stubMeter{}

	if _, err := svc.Fetch(context.Background(), meter, srv.URL); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	calls, committed := meter.snapshot()
	if calls != 1 {
		t.Errorf("reservations = %d, want 1", calls)
	}
	if committed != int64(len(body)) {
		t.Errorf("committed = %d, want %d", committed, len(body))
	}

	// The second call is served from cache and must not be charged again.
	if _, err := svc.Fetch(context.Background(), meter, srv.URL); err != nil {
		t.Fatalf("cached Fetch: %v", err)
	}
	calls, committed = meter.snapshot()
	if calls != 1 {
		t.Errorf("reservations after cache hit = %d, want 1", calls)
	}
	if committed != int64(len(body)) {
		t.Errorf("committed after cache hit = %d, want %d", committed, len(body))
	}
}

func TestFetchRejectedWhenKeyQuotaExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	svc := New(testConfig(t, 5*time.Second, srv.URL), obs.NewMetrics())
	meter := &stubMeter{fail: httpx.QuotaExceeded("fetch quota exceeded for this api key")}

	_, err := svc.Fetch(context.Background(), meter, srv.URL)
	if err == nil {
		t.Fatal("expected the fetch to be rejected")
	}
	apiErr := httpx.AsAPIError(err)
	if apiErr.Status != 402 || apiErr.Type != httpx.TypeInsufficientQuota {
		t.Fatalf("apiErr = %+v, want 402 insufficient_quota", apiErr)
	}
}

func TestServerDailyByteBudgetStopsFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	cfg := testConfig(t, 5*time.Second, srv.URL)
	cfg.Fetch.ServerDailyBytes = 10
	cfg.Fetch.MaxBytes = 1 << 20
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	svc := New(cfg, obs.NewMetrics())

	// The first fetch spends the budget.
	if _, err := svc.Fetch(context.Background(), &stubMeter{}, srv.URL+"/a"); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	// The second one is refused before any traffic is generated.
	_, err := svc.Fetch(context.Background(), &stubMeter{}, srv.URL+"/b")
	if err == nil {
		t.Fatal("expected the fetch to be rejected by the server budget")
	}
	apiErr := httpx.AsAPIError(err)
	if apiErr.Status != 402 {
		t.Fatalf("apiErr = %+v, want 402", apiErr)
	}
}

func TestByteBudgetReservesAndCommits(t *testing.T) {
	b := newByteBudget(100)
	reserved, apiErr := b.reserve(60)
	if apiErr != nil {
		t.Fatalf("reserve: %v", apiErr)
	}
	if reserved != 60 {
		t.Fatalf("reserved = %d, want 60", reserved)
	}
	clamped, apiErr := b.reserve(200)
	if apiErr != nil {
		t.Fatalf("second reserve: %v", apiErr)
	}
	if clamped != 40 {
		t.Fatalf("clamped = %d, want 40", clamped)
	}
	b.commit(reserved, 10)
	b.commit(clamped, 90)
	if used := b.usedBytes(); used != 100 {
		t.Fatalf("used = %d, want 100", used)
	}
	if _, apiErr := b.reserve(1); apiErr == nil {
		t.Fatal("reserve after the budget is spent should be rejected")
	}
}

func TestUnlimitedByteBudget(t *testing.T) {
	b := newByteBudget(0)
	for i := 0; i < 100; i++ {
		if _, apiErr := b.reserve(5 << 20); apiErr != nil {
			t.Fatalf("reserve %d: %v", i, apiErr)
		}
	}
}
