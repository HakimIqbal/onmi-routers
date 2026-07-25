// Package console — in-process ring buffer + SSE broadcaster for live server
// logs. Mirrors 9Router's "Live server console output" panel: gateway log
// lines stream to connected dashboard clients in real time.
//
// The gateway's own slog handler writes into this buffer (see Attach). SSE
// clients subscribe via Subscribe() and receive lines as they arrive.
package console

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
)

// Line is one log entry.
type Line struct {
	TS  string `json:"ts"`
	Lvl string `json:"level"`
	Msg string `json:"msg"`
}

// Console is a thread-safe ring buffer + pub/sub hub.
type Console struct {
	mu      sync.RWMutex
	buf     []Line
	cap     int
	subs    map[chan Line]struct{}
	subMu   sync.RWMutex
}

// New builds a console with the given ring capacity (0 → default 500).
func New(capacity int) *Console {
	if capacity <= 0 {
		capacity = 500
	}
	return &Console{
		buf:  make([]Line, 0, capacity),
		cap:  capacity,
		subs: make(map[chan Line]struct{}),
	}
}

// Write implements io.Writer so it can be used as a slog handler output.
// We parse the slog JSON line for timestamp/level/message.
func (c *Console) Write(p []byte) (int, error) {
	line := parseSlog(p)
	c.append(line)
	c.publish(line)
	return len(p), nil
}

// AppendRaw pushes a pre-formatted line (used for non-slog sources).
func (c *Console) AppendRaw(level, msg string) {
	l := Line{TS: nowStr(), Lvl: level, Msg: msg}
	c.append(l)
	c.publish(l)
}

func (c *Console) append(l Line) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, l)
	if len(c.buf) > c.cap {
		c.buf = c.buf[len(c.buf)-c.cap:]
	}
}

func (c *Console) publish(l Line) {
	c.subMu.RLock()
	defer c.subMu.RUnlock()
	for ch := range c.subs {
		select {
		case ch <- l:
		default:
			// slow subscriber — drop to avoid blocking the hot path
		}
	}
}

// Recent returns the buffered lines (oldest → newest), capped at `n` (0 = all).
func (c *Console) Recent(n int) []Line {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n <= 0 || n >= len(c.buf) {
		out := make([]Line, len(c.buf))
		copy(out, c.buf)
		return out
	}
	out := make([]Line, n)
	copy(out, c.buf[len(c.buf)-n:])
	return out
}

// Subscribe registers a channel that receives live lines. Caller must call
// Unsubscribe when done. The channel should have a small buffer.
func (c *Console) Subscribe(ch chan Line) {
	c.subMu.Lock()
	c.subs[ch] = struct{}{}
	c.subMu.Unlock()
}

// Unsubscribe removes a previously registered channel.
func (c *Console) Unsubscribe(ch chan Line) {
	c.subMu.Lock()
	delete(c.subs, ch)
	c.subMu.Unlock()
}

// parseSlog extracts fields from a slog JSON line. Falls back to raw text.
func parseSlog(p []byte) Line {
	l := Line{TS: nowStr(), Lvl: "INFO"}
	// Best-effort: trim, then try JSON.
	raw := bytes.TrimSpace(p)
	if len(raw) == 0 {
		return l
	}
	var m map[string]any
	if err := jsonUnmarshal(raw, &m); err == nil {
		if ts, ok := m["time"].(string); ok {
			l.TS = ts
		}
		if lv, ok := m["level"].(string); ok {
			l.Lvl = lv
		}
		if msg, ok := m["msg"].(string); ok {
			l.Msg = msg
			// Append key/value pairs for context.
			var extras []string
			for k, v := range m {
				if k == "time" || k == "level" || k == "msg" {
					continue
				}
				extras = append(extras, k+"="+toStr(v))
			}
			if len(extras) > 0 {
				l.Msg += "  " + joinComma(extras)
			}
			return l
		}
	}
	l.Msg = string(raw)
	return l
}

// Handler returns a slog.Handler that writes JSON into this console.
func (c *Console) Handler(level slog.Level) slog.Handler {
	return slog.NewJSONHandler(c, &slog.HandlerOptions{Level: level})
}

// Attach wires this console as the slog default handler (JSON output → buffer).
// Returns the previous handler so callers can restore if needed.
func (c *Console) Attach(level slog.Level) *slog.Logger {
	logger := slog.New(c.Handler(level))
	slog.SetDefault(logger)
	return logger
}

// MultiHandler fans a log record out to multiple slog handlers. Used to send
// gateway logs to both stdout and the live console ring buffer.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler builds a MultiHandler from the given handlers.
func NewMultiHandler(hs ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: hs}
}

func (m *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: next}
}

func (m *MultiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithGroup(name)
	}
	return &MultiHandler{handlers: next}
}

// ctxKey is unused but kept for future per-request tagging.
var _ = context.Background
