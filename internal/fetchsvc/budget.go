package fetchsvc

import (
	"sync"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/httpx"
)

// Meter accounts the upstream traffic produced by one caller (an API key or an
// anonymous client). auth.Key implements it; the service only needs this
// narrow contract so it does not depend on the auth package.
type Meter interface {
	// ReserveFetch books up to maxBytes of the caller's daily fetch budget
	// and charges one fetch. The returned amount must be handed back to
	// CommitFetch once the transferred size is known.
	ReserveFetch(maxBytes int64) (int64, *httpx.APIError)
	// CommitFetch settles a reservation with the bytes actually transferred.
	CommitFetch(reserved, actual int64)
	// AcquireFetch takes one of the caller's concurrent-fetch slots and
	// reports whether one was available.
	AcquireFetch() bool
	// ReleaseFetch returns a concurrent-fetch slot.
	ReleaseFetch()
}

// byteBudget caps the total upstream bytes a service may pull per day. A zero
// limit means unlimited.
type byteBudget struct {
	mu       sync.Mutex
	limit    int64
	day      string
	used     int64
	reserved int64
}

func newByteBudget(limit int64) *byteBudget {
	return &byteBudget{limit: limit, day: time.Now().Format("2006-01-02")}
}

// reserve books up to max bytes of the daily budget.
func (b *byteBudget) reserve(max int64) (int64, *httpx.APIError) {
	if b == nil {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	if b.limit > 0 {
		remaining := b.limit - b.used - b.reserved
		if remaining <= 0 {
			return 0, httpx.QuotaExceeded("server fetch traffic quota exceeded")
		}
		if max > remaining {
			max = remaining
		}
	}
	b.reserved += max
	return max, nil
}

// commit settles a reservation with the bytes actually transferred.
func (b *byteBudget) commit(reserved, actual int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved -= reserved
	if actual < 0 {
		actual = 0
	}
	b.used += actual
}

// usedBytes reports the bytes charged today.
func (b *byteBudget) usedBytes() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.used
}

func (b *byteBudget) rollLocked() {
	today := time.Now().Format("2006-01-02")
	if b.day != today {
		b.day = today
		b.used = 0
		b.reserved = 0
	}
}
