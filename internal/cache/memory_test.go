package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestMemory_ImplementsCache(_ *testing.T) {
	var _ Cache = (*Memory)(nil)
}

func TestMemory_SetAndGet(t *testing.T) {
	c := NewMemory(10, time.Minute)
	resp := &providers.Response{ID: "resp-1", Model: "gpt-4o"}

	c.Set("key1", resp)
	got, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.ID != "resp-1" {
		t.Errorf("expected resp-1, got %s", got.ID)
	}
}

func TestMemory_Miss(t *testing.T) {
	c := NewMemory(10, time.Minute)
	_, ok := c.Get("missing")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestMemory_TTLExpiration(t *testing.T) {
	c := NewMemory(10, 10*time.Millisecond)
	c.Set("key1", &providers.Response{ID: "resp-1"})

	time.Sleep(20 * time.Millisecond)
	_, ok := c.Get("key1")
	if ok {
		t.Error("expected cache miss after TTL")
	}
}

func TestMemory_LRUEviction(t *testing.T) {
	c := NewMemory(2, time.Minute)
	c.Set("a", &providers.Response{ID: "a"})
	c.Set("b", &providers.Response{ID: "b"})
	c.Set("c", &providers.Response{ID: "c"}) // should evict "a"

	if _, ok := c.Get("a"); ok {
		t.Error("expected 'a' to be evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("expected 'b' to be present")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("expected 'c' to be present")
	}
}

func TestMemory_LRUAccessOrder(t *testing.T) {
	c := NewMemory(2, time.Minute)
	c.Set("a", &providers.Response{ID: "a"})
	c.Set("b", &providers.Response{ID: "b"})

	c.Get("a") // access "a" — now "b" is LRU

	c.Set("c", &providers.Response{ID: "c"}) // should evict "b"

	if _, ok := c.Get("a"); !ok {
		t.Error("expected 'a' to be present (recently accessed)")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("expected 'b' to be evicted (LRU)")
	}
}

func TestMemory_Update(t *testing.T) {
	c := NewMemory(10, time.Minute)
	c.Set("key1", &providers.Response{ID: "old"})
	c.Set("key1", &providers.Response{ID: "new"})

	got, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected hit")
	}
	if got.ID != "new" {
		t.Errorf("expected new, got %s", got.ID)
	}
	if c.Len() != 1 {
		t.Errorf("expected len 1, got %d", c.Len())
	}
}

func TestMemory_Delete(t *testing.T) {
	c := NewMemory(10, time.Minute)
	c.Set("key1", &providers.Response{ID: "resp"})
	c.Delete("key1")

	if _, ok := c.Get("key1"); ok {
		t.Error("expected miss after delete")
	}
	if c.Len() != 0 {
		t.Errorf("expected len 0, got %d", c.Len())
	}
}

func TestMemory_Clear(t *testing.T) {
	c := NewMemory(10, time.Minute)
	c.Set("a", &providers.Response{ID: "a"})
	c.Set("b", &providers.Response{ID: "b"})
	c.Clear()

	if c.Len() != 0 {
		t.Errorf("expected len 0 after clear, got %d", c.Len())
	}
}

func TestMemory_Concurrent(_ *testing.T) {
	c := NewMemory(100, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%26))
			c.Set(key, &providers.Response{ID: key})
			c.Get(key)
			c.Len()
		}(i)
	}
	wg.Wait()
}
