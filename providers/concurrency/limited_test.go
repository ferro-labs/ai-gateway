package concurrency_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/concurrency"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// stub is a minimal Provider implementation for testing.
type stub struct {
	name    string
	delay   time.Duration
	callsFn func()
	resp    *core.Response
	err     error

	inFlight atomic.Int64
	peak     atomic.Int64
}

func (s *stub) Name() string                       { return s.name }
func (s *stub) SupportedModels() []string          { return nil }
func (s *stub) SupportsModel(_ string) bool        { return true }
func (s *stub) Models() []core.ModelInfo           { return nil }
func (s *stub) Complete(ctx context.Context, _ core.Request) (*core.Response, error) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		cur := s.peak.Load()
		if n <= cur || s.peak.CompareAndSwap(cur, n) {
			break
		}
	}
	if s.callsFn != nil {
		s.callsFn()
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.resp, s.err
}

func TestUnderLimit_NoBlocking(t *testing.T) {
	s := &stub{name: "test", resp: &core.Response{}}
	lp := concurrency.Wrap(s, 5, 100)
	defer lp.Close()

	resp, err := lp.Complete(context.Background(), core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestAtLimit_Queuing(t *testing.T) {
	const workers = 2
	const total = 10

	blocker := make(chan struct{})
	released := atomic.Int64{}
	s := &stub{
		name: "test",
		resp: &core.Response{},
		callsFn: func() {
			<-blocker
			released.Add(1)
		},
	}
	lp := concurrency.Wrap(s, workers, total*2)
	defer lp.Close()

	var wg sync.WaitGroup
	errs := make([]error, total)
	for i := range total {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = lp.Complete(context.Background(), core.Request{})
		}(i)
	}

	// Give goroutines time to enqueue.
	time.Sleep(50 * time.Millisecond)
	close(blocker)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d failed: %v", i, err)
		}
	}
	if s.peak.Load() > workers {
		t.Errorf("peak in-flight %d exceeded worker count %d", s.peak.Load(), workers)
	}
}

func TestContextCancel_MidWait(t *testing.T) {
	blocker := make(chan struct{})
	s := &stub{
		name: "test",
		resp: &core.Response{},
		callsFn: func() { <-blocker },
	}
	lp := concurrency.Wrap(s, 1, 10)
	defer func() {
		close(blocker)
		lp.Close()
	}()

	// Fill the single worker.
	go lp.Complete(context.Background(), core.Request{}) //nolint:errcheck

	// Give worker time to start.
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := lp.Complete(ctx, core.Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got: %v", err)
	}
}

func TestProviderError_PassedThrough(t *testing.T) {
	want := errors.New("upstream unavailable")
	s := &stub{name: "test", err: want}
	lp := concurrency.Wrap(s, 2, 10)
	defer lp.Close()

	_, err := lp.Complete(context.Background(), core.Request{})
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

func TestClose_ShutdownError(t *testing.T) {
	s := &stub{name: "test", resp: &core.Response{}, delay: 200 * time.Millisecond}
	lp := concurrency.Wrap(s, 1, 10)
	lp.Close()

	// After close, new requests should get a shutdown error.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := lp.Complete(ctx, core.Request{})
	if err == nil {
		t.Fatal("expected error after Close, got nil")
	}
}
