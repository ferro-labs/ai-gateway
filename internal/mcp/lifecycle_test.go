package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowClient records when it was closed and can stall to model a wedged stdio
// subprocess working through the transport's graceful/SIGTERM/SIGKILL ladder.
type slowClient struct {
	closeDelay time.Duration
	closed     atomic.Bool
}

func (s *slowClient) Initialize(context.Context) (*ServerInfo, error) { return &ServerInfo{}, nil }
func (s *slowClient) ListTools(context.Context) ([]Tool, error)       { return nil, nil }
func (s *slowClient) CallTool(context.Context, string, json.RawMessage) (*ToolCallResult, error) {
	return &ToolCallResult{}, nil
}
func (s *slowClient) Close() error {
	time.Sleep(s.closeDelay)
	s.closed.Store(true)
	return nil
}

// registryWith installs pre-built clients without going through RegisterConfig,
// which would spawn real transports.
func registryWith(clients map[string]mcpClient) *Registry {
	r := NewRegistry()
	for name, c := range clients {
		r.serverIndex[name] = len(r.regOrder)
		r.regOrder = append(r.regOrder, name)
		r.servers[name] = &serverEntry{config: ServerConfig{Name: name}, client: c, ready: true}
	}
	return r
}

// P2-2: each stdio Close can spend seconds in the transport's shutdown ladder.
// Closing serially multiplies that by the number of servers, and Gateway.Close
// already has only a 5s budget — several wedged servers blow past an
// orchestrator's grace period, which SIGKILLs the pod and re-orphans every
// subprocess the group sweep exists to reap.
func TestRegistryCloseIsConcurrent(t *testing.T) {
	const (
		servers = 6
		delay   = 200 * time.Millisecond
	)

	clients := make(map[string]mcpClient, servers)
	all := make([]*slowClient, 0, servers)
	for i := range servers {
		c := &slowClient{closeDelay: delay}
		all = append(all, c)
		clients[string(rune('a'+i))] = c
	}
	reg := registryWith(clients)

	start := time.Now()
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	// Serial would be servers*delay (1.2s). Allow generous slack for CI.
	if elapsed >= servers*delay {
		t.Errorf("Close took %v for %d servers at %v each — clients are being closed serially",
			elapsed, servers, delay)
	}
	for i, c := range all {
		if !c.closed.Load() {
			t.Errorf("client %d was not closed", i)
		}
	}
}

// P1-2: Route snapshots the registry at request entry and uses it after
// releasing the gateway lock. A config reload that closes the old registry
// immediately terminates the stdio subprocess underneath an in-flight tool call,
// which then fails with "transport closed". Retirement must wait for holders.
func TestRegistryCloseWaitsForInFlightHolders(t *testing.T) {
	c := &slowClient{}
	reg := registryWith(map[string]mcpClient{"s1": c})

	release := reg.Acquire()

	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The holder is still in-flight, so the subprocess must still be alive.
	time.Sleep(50 * time.Millisecond)
	if c.closed.Load() {
		t.Fatal("registry closed its clients while a request still held a snapshot: " +
			"an in-flight tool call would see its subprocess terminated")
	}

	release()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.closed.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("registry never closed its clients after the last holder released")
}

// Acquire after Close must not resurrect a retiring registry, and its release
// must not double-close.
func TestRegistryAcquireAfterCloseIsInert(t *testing.T) {
	c := &slowClient{}
	reg := registryWith(map[string]mcpClient{"s1": c})

	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !c.closed.Load() {
		t.Fatal("expected an unheld registry to close immediately")
	}

	release := reg.Acquire()
	release()
	release() // idempotent
}

// Close detaches each entry's client so a second teardown cannot re-close the
// same transport. An initialisation already in flight holds no reference of its
// own, so re-reading entry.client across its unlocked handshake nil-dereferenced
// and crashed the process. Reproduced under parallel package load.
func TestRegistryCloseDuringInitializeDoesNotPanic(t *testing.T) {
	for range 20 {
		srv := newMockServer(t, []Tool{{Name: "t1"}})

		reg := NewRegistry()
		reg.RegisterConfig(ServerConfig{Name: "s1", URL: srv.URL, TimeoutSeconds: 5})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			reg.InitializeAll(context.Background(), func(string, error) {})
		}()
		go func() {
			defer wg.Done()
			_ = reg.Close()
		}()
		wg.Wait()
		srv.Close()
	}
}

// Concurrent Acquire/release against Close must not deadlock or double-close.
func TestRegistryLifecycleUnderConcurrency(t *testing.T) {
	c := &slowClient{}
	reg := registryWith(map[string]mcpClient{"s1": c})

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := reg.Acquire()
			time.Sleep(time.Millisecond)
			release()
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		_ = reg.Close()
	}()

	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.closed.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("registry never closed after all holders released")
}
