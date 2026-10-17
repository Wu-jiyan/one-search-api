package limit

import (
	"context"
	"testing"
	"time"
)

func TestBucketUnlimited(t *testing.T) {
	var b *Bucket
	for i := 0; i < 100; i++ {
		if ok, _ := b.Allow(); !ok {
			t.Fatal("nil bucket must always allow")
		}
	}
}

func TestBucketExhaustion(t *testing.T) {
	b := NewBucket(60, 2)
	if ok, _ := b.Allow(); !ok {
		t.Fatal("first request should pass")
	}
	if ok, _ := b.Allow(); !ok {
		t.Fatal("second request should pass")
	}
	ok, wait := b.Allow()
	if ok {
		t.Fatal("third request should be limited")
	}
	if wait <= 0 {
		t.Errorf("wait = %v, want > 0", wait)
	}
}

func TestBreaker(t *testing.T) {
	b := NewBreaker(2, 20*time.Millisecond)
	if !b.Allow() {
		t.Fatal("closed breaker must allow")
	}
	b.Failure()
	if !b.Allow() {
		t.Fatal("one failure below threshold must still allow")
	}
	b.Failure()
	if b.Allow() {
		t.Fatal("breaker should be open after threshold")
	}
	if b.State() != StateOpen {
		t.Errorf("state = %v, want open", b.State())
	}
	time.Sleep(25 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("half-open breaker should allow a probe")
	}
	if b.Allow() {
		t.Fatal("half-open breaker must allow only one probe")
	}
	b.Success()
	if !b.Allow() {
		t.Fatal("successful probe must close the breaker")
	}
	if b.State() != StateClosed {
		t.Errorf("state = %v, want closed", b.State())
	}
}

func TestSemaphore(t *testing.T) {
	s := NewSemaphore(1)
	if !s.TryAcquire() {
		t.Fatal("first acquire should succeed")
	}
	if s.TryAcquire() {
		t.Fatal("second acquire should fail")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Fatal("acquire after release should succeed")
	}
	s.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	s.Release()
}
