// Package limit provides the small concurrency primitives used to protect the
// service and its upstreams: token buckets, a try-acquire semaphore and a
// circuit breaker.
package limit

import (
	"context"
	"sync"
	"time"
)

// Bucket is a token bucket. A nil *Bucket is valid and always allows.
type Bucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // tokens per second
	last     time.Time
}

// NewBucket creates a bucket refilling at rpm requests per minute with the
// given burst capacity. rpm <= 0 returns nil (unlimited).
func NewBucket(rpm, burst int) *Bucket {
	if rpm <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = max(1, rpm/10)
	}
	return &Bucket{
		tokens:   float64(burst),
		capacity: float64(burst),
		rate:     float64(rpm) / 60.0,
		last:     time.Now(),
	}
}

// Allow consumes one token. If none is available it returns the wait duration
// until the next token becomes available.
func (b *Bucket) Allow() (bool, time.Duration) {
	if b == nil {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = min(b.capacity, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing / b.rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Second
	}
	return false, wait
}

// Tokens reports the currently available tokens (for tests/diagnostics).
func (b *Bucket) Tokens() float64 {
	if b == nil {
		return -1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}

// Semaphore is a counting semaphore with non-blocking acquisition.
type Semaphore struct {
	ch chan struct{}
}

// NewSemaphore creates a semaphore with n slots. n <= 0 returns nil (unlimited).
func NewSemaphore(n int) *Semaphore {
	if n <= 0 {
		return nil
	}
	return &Semaphore{ch: make(chan struct{}, n)}
}

// TryAcquire attempts to take a slot without blocking.
func (s *Semaphore) TryAcquire() bool {
	if s == nil {
		return true
	}
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release returns a slot.
func (s *Semaphore) Release() {
	if s == nil {
		return
	}
	select {
	case <-s.ch:
	default:
	}
}

// Acquire takes a slot, waiting for ctx or a free slot.
func (s *Semaphore) Acquire(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BreakerState is the exported state of a Breaker.
type BreakerState int

const (
	StateClosed BreakerState = iota
	StateHalfOpen
	StateOpen
)

// String renders the state for metrics/logging.
func (s BreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half_open"
	default:
		return "open"
	}
}

// Breaker is a consecutive-failure circuit breaker.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	failures  int
	openAt    time.Time
	probing   bool
}

// NewBreaker creates a breaker that opens after threshold consecutive failures
// and probes again after cooldown.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// Allow reports whether a request may proceed. In the half-open state only one
// probe at a time is allowed.
func (b *Breaker) Allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openAt.IsZero() {
		return true
	}
	if time.Since(b.openAt) < b.cooldown {
		return false
	}
	if b.probing {
		return false
	}
	b.probing = true
	return true
}

// Success records a successful call and closes the breaker.
func (b *Breaker) Success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openAt = time.Time{}
	b.probing = false
}

// Failure records a failed call, possibly opening the breaker.
func (b *Breaker) Failure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	b.failures++
	if b.failures >= b.threshold {
		b.openAt = time.Now()
		b.failures = 0
	}
}

// State returns the current breaker state.
func (b *Breaker) State() BreakerState {
	if b == nil {
		return StateClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openAt.IsZero() {
		return StateClosed
	}
	if time.Since(b.openAt) < b.cooldown {
		return StateOpen
	}
	return StateHalfOpen
}
