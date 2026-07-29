// Package memory — lightweight session memory (FTS-ish exact store) for agent context.
// In-process only; optional Redis later. Not a full vector DB.
package memory

import (
	"strings"
	"sync"
	"time"
)

type Item struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	mu    sync.RWMutex
	items []Item
	max   int
	seq   int
}

func New(max int) *Store {
	if max <= 0 {
		max = 200
	}
	return &Store{max: max}
}

func (s *Store) Add(key, content string) Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	it := Item{
		ID:        time.Now().UTC().Format("20060102") + "-" + itoa(s.seq),
		Key:       strings.TrimSpace(key),
		Content:   strings.TrimSpace(content),
		CreatedAt: time.Now().UTC(),
	}
	s.items = append(s.items, it)
	if len(s.items) > s.max {
		s.items = s.items[len(s.items)-s.max:]
	}
	return it
}

func (s *Store) Search(q string, limit int) []Item {
	if limit <= 0 {
		limit = 10
	}
	q = strings.ToLower(strings.TrimSpace(q))
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Item
	for i := len(s.items) - 1; i >= 0 && len(out) < limit; i-- {
		it := s.items[i]
		if q == "" || strings.Contains(strings.ToLower(it.Key+" "+it.Content), q) {
			out = append(out, it)
		}
	}
	return out
}

func (s *Store) Stats() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{"entries": len(s.items), "max": s.max}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
