// Package cache — in-process semantic/exact response cache for chat completions.
// Keyed by SHA256(model + normalized messages). TTL + max entries. Fail-open.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

type entry struct {
	body      []byte
	expiresAt time.Time
}

// Store is a TTL LRU-ish map (simple max-N eviction of oldest).
type Store struct {
	mu      sync.RWMutex
	items   map[string]entry
	order   []string
	ttl     time.Duration
	max     int
	hits    int64
	misses  int64
	enabled bool
}

func New(ttl time.Duration, max int) *Store {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if max <= 0 {
		max = 256
	}
	return &Store{items: map[string]entry{}, ttl: ttl, max: max, enabled: true}
}

func (s *Store) SetEnabled(v bool) { s.mu.Lock(); s.enabled = v; s.mu.Unlock() }
func (s *Store) Enabled() bool     { s.mu.RLock(); defer s.mu.RUnlock(); return s.enabled }

func (s *Store) Stats() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"enabled": s.enabled,
		"entries": len(s.items),
		"max":     s.max,
		"ttl_sec": int(s.ttl.Seconds()),
		"hits":    s.hits,
		"misses":  s.misses,
	}
}

// KeyFromChat builds a stable key from model + messages.
func KeyFromChat(model string, body map[string]any) string {
	payload := map[string]any{"model": model, "messages": body["messages"]}
	if t, ok := body["temperature"]; ok {
		payload["temperature"] = t
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Store) Get(key string) ([]byte, bool) {
	if s == nil || key == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		s.misses++
		return nil, false
	}
	e, ok := s.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		if ok {
			delete(s.items, key)
		}
		s.misses++
		return nil, false
	}
	s.hits++
	out := make([]byte, len(e.body))
	copy(out, e.body)
	return out, true
}

func (s *Store) Put(key string, body []byte) {
	if s == nil || key == "" || len(body) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	if _, ok := s.items[key]; !ok {
		s.order = append(s.order, key)
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	s.items[key] = entry{body: cp, expiresAt: time.Now().Add(s.ttl)}
	for len(s.items) > s.max && len(s.order) > 0 {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.items, old)
	}
}
