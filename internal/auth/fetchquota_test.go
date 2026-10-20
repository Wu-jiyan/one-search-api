package auth

import (
	"testing"

	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/httpx"
)

func quotaConfig(bytes int64, count int, concurrency int) config.AuthConfig {
	return config.AuthConfig{
		Mode:                "key",
		FetchMaxConcurrency: concurrency,
		Keys: []config.KeyConfig{{
			Name:            "quota",
			HashedToken:     config.HashToken("tok"),
			RPM:             1000,
			Burst:           100,
			DailyQuota:      1000,
			MaxResults:      5,
			FetchDailyBytes: bytes,
			FetchDailyCount: count,
		}},
	}
}

func TestReserveFetchChargesBytes(t *testing.T) {
	a := New(quotaConfig(100, 0, 0))
	key, apiErr := a.Authenticate("tok", "1.1.1.1")
	if apiErr != nil {
		t.Fatalf("Authenticate: %v", apiErr)
	}

	reserved, apiErr := key.ReserveFetch(60)
	if apiErr != nil {
		t.Fatalf("ReserveFetch: %v", apiErr)
	}
	if reserved != 60 {
		t.Fatalf("reserved = %d, want 60", reserved)
	}

	// The second reservation is clamped to what is left of the budget.
	clamped, apiErr := key.ReserveFetch(200)
	if apiErr != nil {
		t.Fatalf("second ReserveFetch: %v", apiErr)
	}
	if clamped != 40 {
		t.Fatalf("clamped reservation = %d, want 40", clamped)
	}

	// Settling with the real sizes charges the transferred bytes.
	key.CommitFetch(reserved, 10)
	key.CommitFetch(clamped, 90)
	count, used := key.FetchUsage()
	if used != 100 {
		t.Fatalf("used bytes = %d, want 100", used)
	}
	if count != 2 {
		t.Fatalf("fetch count = %d, want 2", count)
	}

	// The budget is spent: no further fetch may start.
	if _, apiErr := key.ReserveFetch(1); apiErr == nil {
		t.Fatal("reservation after the budget is spent should be rejected")
	} else if apiErr.Status != 402 || apiErr.Type != httpx.TypeInsufficientQuota {
		t.Fatalf("apiErr = %+v, want 402 insufficient_quota", apiErr)
	}
}

func TestReserveFetchEnforcesCount(t *testing.T) {
	a := New(quotaConfig(0, 2, 0))
	key, _ := a.Authenticate("tok", "1.1.1.1")

	for i := 0; i < 2; i++ {
		if _, apiErr := key.ReserveFetch(1); apiErr != nil {
			t.Fatalf("reservation %d: %v", i, apiErr)
		}
	}
	if _, apiErr := key.ReserveFetch(1); apiErr == nil {
		t.Fatal("third reservation should be rejected by the fetch count limit")
	}
}

func TestFetchConcurrencyLimit(t *testing.T) {
	a := New(quotaConfig(0, 0, 2))
	key, _ := a.Authenticate("tok", "1.1.1.1")

	if !key.AcquireFetch() || !key.AcquireFetch() {
		t.Fatal("expected two concurrent fetch slots")
	}
	if key.AcquireFetch() {
		t.Error("third concurrent fetch should be refused")
	}
	key.ReleaseFetch()
	if !key.AcquireFetch() {
		t.Error("slot should be reusable after release")
	}
}

func TestUnlimitedFetchBudget(t *testing.T) {
	a := New(quotaConfig(0, 0, 0))
	key, _ := a.Authenticate("tok", "1.1.1.1")

	for i := 0; i < 100; i++ {
		if _, apiErr := key.ReserveFetch(5 << 20); apiErr != nil {
			t.Fatalf("reservation %d rejected: %v", i, apiErr)
		}
		key.CommitFetch(5<<20, 1024)
	}
}
