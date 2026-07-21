package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// markedConfigBody builds a config carrying a unique marker in the target
// weight, so a history entry can be matched against the config it claims to
// record.
func markedConfigBody(marker int) string {
	return fmt.Sprintf(`{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai","weight":%d}]}`, marker)
}

func configMarker(cfg aigateway.Config) float64 {
	if len(cfg.Targets) == 0 {
		return 0
	}
	return cfg.Targets[0].Weight
}

// TestConfigMutationsAreAtomicUnderConcurrency drives concurrent config updates
// and rollbacks through the admin API and asserts the three invariants that a
// non-atomic apply-then-record breaks:
//
//  1. the newest history entry names the config that is actually active;
//  2. history versions are strictly monotonic with no gaps or duplicates;
//  3. a rollback's recorded provenance is the version it actually replaced.
//
// Run with -race.
func TestConfigMutationsAreAtomicUnderConcurrency(t *testing.T) {
	cm := &latentConfigManager{cfg: singleConfig()}
	cm.initial = cm.cfg
	h, r := setupTestRouterWithConfigManager(cm)
	adminKey := createAdminKey(t, h)

	// Seed version 1 so every concurrent rollback resolves to a real version.
	doAdminRequest(t, r, http.MethodPut, "/admin/config", markedConfigBody(1), adminKey, http.StatusOK)

	const (
		workers    = 8
		iterations = 6
	)

	// Release every worker at once so they also finish clustered: an uncontested
	// tail would let the final mutation record itself correctly by luck.
	start := make(chan struct{})

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := range iterations {
				switch (worker + i) % 4 {
				case 2:
					doAdminRequest(t, r, http.MethodPost, "/admin/config/rollback/1", "", adminKey, http.StatusOK)
				case 3:
					// DELETE resets and then reads the config back to record it,
					// so it diverges too unless the whole mutation is serialized.
					doAdminRequest(t, r, http.MethodDelete, "/admin/config", "", adminKey, http.StatusOK)
				default:
					marker := (worker+1)*100 + i + 1
					doAdminRequest(t, r, http.MethodPut, "/admin/config", markedConfigBody(marker), adminKey, http.StatusOK)
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	history := h.getConfigHistorySnapshot()
	if len(history) != 1+workers*iterations {
		t.Fatalf("history len = %d, want %d", len(history), 1+workers*iterations)
	}

	active := configMarker(cm.GetConfig())
	recorded := configMarker(history[len(history)-1].Config)
	if active != recorded {
		t.Errorf("newest history entry records config marker %v but the active config is %v", recorded, active)
	}

	for i, entry := range history {
		if entry.Version != i+1 {
			t.Fatalf("history[%d].Version = %d, want %d (versions must be strictly monotonic)", i, entry.Version, i+1)
		}
		if entry.RolledBackFrom == nil {
			continue
		}
		// A rollback replaces whatever was latest at the moment it ran, and its
		// own entry is appended directly after that one. Any other value means
		// the provenance was read outside the mutation lock and went stale.
		if want := entry.Version - 1; *entry.RolledBackFrom != want {
			t.Errorf("history[%d] rolled_back_from = %d, want %d (stale provenance)", i, *entry.RolledBackFrom, want)
		}
	}
}

// TestGatewayConfigManagerPersistsWhatItApplies proves the manager never leaves
// one config persisted while a different one is active. That divergence
// survives a restart: ReloadConfig persists before it applies, so two writers
// can commit config B to storage yet leave config A running — and the next boot
// loads B, bringing the process up on a config it never served.
//
// The two writers are ordered so the divergence is deterministic rather than
// left to scheduling luck: the slow writer persists first but stalls before it
// can apply, letting the fast writer persist second and apply first.
//
// Run with -race.
func TestGatewayConfigManagerPersistsWhatItApplies(t *testing.T) {
	gw, err := newTestGateway(t, singleConfig())
	if err != nil {
		t.Fatalf("new test gateway: %v", err)
	}

	const slowMarker, fastMarker = 1.0, 2.0
	store := &orderedConfigStore{
		slowMarker:    slowMarker,
		slowEntered:   make(chan struct{}),
		slowPersisted: make(chan struct{}),
	}
	m, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	reload := func(marker float64) {
		cfg := singleConfig()
		cfg.Targets[0].Weight = marker
		if err := m.ReloadConfig(context.Background(), cfg); err != nil {
			t.Errorf("reload config %v: %v", marker, err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); reload(slowMarker) }()

	// Start the second writer only once the first is provably inside the
	// mutation path, so the interleave under test does not depend on which
	// goroutine the scheduler happens to run first.
	<-store.slowEntered

	wg.Add(1)
	go func() { defer wg.Done(); reload(fastMarker) }()
	wg.Wait()

	persisted, ok := store.last()
	if !ok {
		t.Fatal("no config was persisted")
	}
	if got, want := configMarker(persisted), configMarker(m.GetConfig()); got != want {
		t.Fatalf("persisted config marker %v but active config marker %v: a restart would adopt a config the gateway never served", got, want)
	}
}

func doAdminRequest(t *testing.T, r http.Handler, method, url, body string, key *APIKey, wantStatus int) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(method, url, body, key))
	if w.Code != wantStatus {
		t.Errorf("%s %s = %d, want %d: %s", method, url, w.Code, wantStatus, w.Body.String())
	}
}

// latentConfigManager is a ConfigManager whose apply path keeps working after
// the config has become active — as the real one does, since
// Gateway.ReloadConfig installs the config and then tears down the previous
// plugin manager before returning. The pause makes that window observable
// instead of vanishingly small.
type latentConfigManager struct {
	mu      sync.Mutex
	cfg     aigateway.Config
	initial aigateway.Config
}

func (m *latentConfigManager) GetConfig() aigateway.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *latentConfigManager) ReloadConfig(_ context.Context, cfg aigateway.Config) error {
	if err := aigateway.ValidateConfig(cfg); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	return nil
}

func (m *latentConfigManager) ResetConfig(_ context.Context) error {
	m.mu.Lock()
	m.cfg = m.initial
	m.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	return nil
}

func (m *latentConfigManager) Ping(_ context.Context) error { return nil }

// orderedConfigStore is an in-memory ConfigStore that remembers the last config
// written and forces a fixed persist order between two writers: the config
// marked slowMarker persists first and then stalls, which is the window a real
// store's transaction opens between persisting a config and the caller applying
// it. Every other config waits for that first persist before committing.
type orderedConfigStore struct {
	mu     sync.Mutex
	saved  aigateway.Config
	hasCfg bool

	slowMarker    float64
	slowEntered   chan struct{}
	slowPersisted chan struct{}
}

func (s *orderedConfigStore) Save(_ context.Context, cfg aigateway.Config) error {
	if configMarker(cfg) != s.slowMarker {
		// slowPersisted is already closed once mutations are serialized, so this
		// only blocks while the slow writer genuinely still holds the window.
		<-s.slowPersisted
		s.record(cfg)
		return nil
	}

	close(s.slowEntered)
	s.record(cfg)
	close(s.slowPersisted)
	// Persisted but not yet applied. Serializing the mutation is what keeps the
	// other writer out of this window.
	time.Sleep(50 * time.Millisecond)
	return nil
}

func (s *orderedConfigStore) record(cfg aigateway.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved, s.hasCfg = cfg, true
}

func (s *orderedConfigStore) Load(_ context.Context) (aigateway.Config, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved, s.hasCfg, nil
}

func (s *orderedConfigStore) Delete(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved, s.hasCfg = aigateway.Config{}, false
	return nil
}

func (s *orderedConfigStore) last() (aigateway.Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved, s.hasCfg
}
