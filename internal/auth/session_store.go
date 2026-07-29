package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionStore maps a random session token (cookie value) → underlying gateway API key.
// P3-3: cookie is NOT the raw API key. Sessions are persisted in Redis so they
// survive process restarts / deploys. TTL is IDLE-based (from lastSeen), not
// absolute (from createdAt) — a session only expires after SessionIdleTTL of
// inactivity, so daily use never gets logged out by a deploy.
type SessionStore struct {
	mu      sync.Mutex // guards local fallback only
	rdb     *redis.Client
	ctx     context.Context
	local   map[string]*session // in-memory fallback when Redis is unavailable
}

type session struct {
	key       string
	createdAt time.Time
	lastSeen  time.Time
}

// SessionIdleTTL is the maximum idle lifetime of a session (30 days).
// Reset on every Lookup hit (lastSeen slides forward).
const SessionIdleTTL = 30 * 24 * time.Hour

const sessionKeyPrefix = "sess:"

// NewSessionStore creates a Redis-backed session store. If rdb is nil the store
// degrades to an in-memory map (sessions lost on restart, but still functional).
func NewSessionStore(rdb *redis.Client) *SessionStore {
	s := &SessionStore{
		rdb:   rdb,
		ctx:   context.Background(),
		local: make(map[string]*session),
	}
	// Background cleanup only needed for the local fallback.
	if rdb == nil {
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				s.cleanup()
			}
		}()
	}
	return s
}

// Create issues a new session token bound to the given API key.
func (s *SessionStore) Create(apiKey string) (string, error) {
	buf := make([]byte, 32) // 256-bit
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	token := hex.EncodeToString(buf)
	now := time.Now()
	if s.rdb != nil {
		val := fmt.Sprintf("%s|%d|%d", apiKey, now.Unix(), now.Unix())
		if err := s.rdb.Set(s.ctx, sessionKeyPrefix+token, val, SessionIdleTTL).Err(); err != nil {
			// fall back to local
			s.mu.Lock()
			s.local[token] = &session{key: apiKey, createdAt: now, lastSeen: now}
			s.mu.Unlock()
		}
		return token, nil
	}
	s.mu.Lock()
	s.local[token] = &session{key: apiKey, createdAt: now, lastSeen: now}
	s.mu.Unlock()
	return token, nil
}

// Lookup returns the API key bound to the session token, or empty string.
// Slides lastSeen forward on hit (extends idle TTL while in use).
func (s *SessionStore) Lookup(token string) string {
	if token == "" {
		return ""
	}
	if s.rdb != nil {
		key := sessionKeyPrefix + token
		val, err := s.rdb.Get(s.ctx, key).Result()
		if err != nil {
			return "" // missing or expired
		}
		apiKey, createdAt, lastSeen := parseSessionVal(val)
		if apiKey == "" {
			return ""
		}
		now := time.Now()
		// idle expiry
		if now.Sub(time.Unix(lastSeen, 0)) > SessionIdleTTL {
			s.rdb.Del(s.ctx, key)
			return ""
		}
		// slide lastSeen forward
		updated := fmt.Sprintf("%s|%d|%d", apiKey, createdAt, now.Unix())
		s.rdb.Set(s.ctx, key, updated, SessionIdleTTL)
		return apiKey
	}
	// local fallback
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.local[token]
	if !ok {
		return ""
	}
	if time.Since(sess.lastSeen) > SessionIdleTTL {
		delete(s.local, token)
		return ""
	}
	sess.lastSeen = time.Now()
	return sess.key
}

// Revoke deletes a session (logout).
func (s *SessionStore) Revoke(token string) {
	if token == "" {
		return
	}
	if s.rdb != nil {
		s.rdb.Del(s.ctx, sessionKeyPrefix+token)
		return
	}
	s.mu.Lock()
	delete(s.local, token)
	s.mu.Unlock()
}

func (s *SessionStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for token, sess := range s.local {
		if now.Sub(sess.lastSeen) > SessionIdleTTL {
			delete(s.local, token)
		}
	}
}

// parseSessionVal parses "apiKey|createdAt|lastSeen".
func parseSessionVal(val string) (apiKey string, createdAt, lastSeen int64) {
	var cs, ls int64
	_, _ = fmt.Sscanf(val, "%[^|]|%d|%d", &apiKey, &cs, &ls)
	createdAt, lastSeen = cs, ls
	return
}
