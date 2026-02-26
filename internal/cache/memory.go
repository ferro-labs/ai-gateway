package cache

import (
	"container/list"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
)

type memoryEntry struct {
	key       string
	response  *providers.Response
	expiresAt time.Time
}

// Memory is a thread-safe in-memory LRU cache with TTL expiration.
type Memory struct {
	mu         sync.Mutex
	capacity   int
	ttl        time.Duration
	items      map[string]*list.Element
	evictList  *list.List
}

// NewMemory creates a new in-memory LRU cache.
func NewMemory(capacity int, ttl time.Duration) *Memory {
	return &Memory{
		capacity:  capacity,
		ttl:       ttl,
		items:     make(map[string]*list.Element),
		evictList: list.New(),
	}
}

// Get returns the cached response for key, or false if missing or expired.
func (m *Memory) Get(key string) (*providers.Response, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	elem, ok := m.items[key]
	if !ok {
		return nil, false
	}

	entry := elem.Value.(*memoryEntry)
	if time.Now().After(entry.expiresAt) {
		m.removeElement(elem)
		return nil, false
	}

	m.evictList.MoveToFront(elem)
	return entry.response, true
}

// Set stores a response in the cache with the configured TTL.
func (m *Memory) Set(key string, resp *providers.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if elem, ok := m.items[key]; ok {
		m.evictList.MoveToFront(elem)
		entry := elem.Value.(*memoryEntry)
		entry.response = resp
		entry.expiresAt = time.Now().Add(m.ttl)
		return
	}

	if m.evictList.Len() >= m.capacity {
		m.removeOldest()
	}

	entry := &memoryEntry{
		key:       key,
		response:  resp,
		expiresAt: time.Now().Add(m.ttl),
	}
	elem := m.evictList.PushFront(entry)
	m.items[key] = elem
}

// Delete removes an entry from the cache.
func (m *Memory) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if elem, ok := m.items[key]; ok {
		m.removeElement(elem)
	}
}

// Len returns the number of entries currently in the cache.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evictList.Len()
}

// Clear removes all entries from the cache.
func (m *Memory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = make(map[string]*list.Element)
	m.evictList.Init()
}

func (m *Memory) removeOldest() {
	elem := m.evictList.Back()
	if elem != nil {
		m.removeElement(elem)
	}
}

func (m *Memory) removeElement(elem *list.Element) {
	m.evictList.Remove(elem)
	entry := elem.Value.(*memoryEntry)
	delete(m.items, entry.key)
}
