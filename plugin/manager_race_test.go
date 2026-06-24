package plugin

import (
	"context"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// TestManager_ConcurrentRegisterAndRunBefore verifies that concurrent calls to
// Register and RunBefore do not race. Requires -race to detect the defect: the
// fix adds a lock to Register but RunBefore ranges over m.before without
// holding m.mu.RLock(), so the goroutine scheduler can interleave a slice
// grow (in Register) with the range read (in RunBefore).
func TestManager_ConcurrentRegisterAndRunBefore(_ *testing.T) {
	m := NewManager()
	pctx := NewContext(&providers.Request{Model: "gpt-4o"})

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "p", typ: TypeGuardrail})
		}()
		go func() {
			defer wg.Done()
			_ = m.RunBefore(context.Background(), pctx)
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentRegisterAndRunAfter is the same race check for
// RunAfter / m.after.
func TestManager_ConcurrentRegisterAndRunAfter(_ *testing.T) {
	m := NewManager()
	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	pctx.Response = &providers.Response{ID: "r1"}

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageAfterRequest, &mockPlugin{name: "p", typ: TypeLogging})
		}()
		go func() {
			defer wg.Done()
			_ = m.RunAfter(context.Background(), pctx)
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentRegisterAndRunOnError is the same race check for
// RunOnError / m.onErr.
func TestManager_ConcurrentRegisterAndRunOnError(_ *testing.T) {
	m := NewManager()
	pctx := NewContext(&providers.Request{Model: "gpt-4o"})

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageOnError, &mockPlugin{name: "p", typ: TypeLogging})
		}()
		go func() {
			defer wg.Done()
			m.RunOnError(context.Background(), pctx)
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentRegisterAndHasPlugins checks that HasPlugins does not
// race with concurrent Register calls (it reads all three slice lengths without
// a lock).
func TestManager_ConcurrentRegisterAndHasPlugins(_ *testing.T) {
	m := NewManager()

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "p", typ: TypeGuardrail})
		}()
		go func() {
			defer wg.Done()
			_ = m.HasPlugins()
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentAllReadersAndRegister fires RunBefore, RunAfter,
// RunOnError, and HasPlugins simultaneously against concurrent Register calls.
// This catches any reader–reader or reader–writer races that the individual
// pairwise tests might miss under a specific schedule.
func TestManager_ConcurrentAllReadersAndRegister(_ *testing.T) {
	m := NewManager()
	pctxBefore := NewContext(&providers.Request{Model: "gpt-4o"})
	pctxAfter := NewContext(&providers.Request{Model: "gpt-4o"})
	pctxAfter.Response = &providers.Response{ID: "r1"}
	pctxErr := NewContext(&providers.Request{Model: "gpt-4o"})

	var wg sync.WaitGroup
	const n = 100

	for i := 0; i < n; i++ {
		wg.Add(5)
		go func() {
			defer wg.Done()
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "p", typ: TypeGuardrail})
		}()
		go func() {
			defer wg.Done()
			_ = m.RunBefore(context.Background(), pctxBefore)
		}()
		go func() {
			defer wg.Done()
			_ = m.RunAfter(context.Background(), pctxAfter)
		}()
		go func() {
			defer wg.Done()
			m.RunOnError(context.Background(), pctxErr)
		}()
		go func() {
			defer wg.Done()
			_ = m.HasPlugins()
		}()
	}
	wg.Wait()
}
