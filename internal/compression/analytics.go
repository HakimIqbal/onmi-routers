package compression

import (
	"sync"
	"time"
)

// ── Compression analytics (Tier 1) ─────────────────────────────────────────
// An in-memory, thread-safe tracker of real measured compression savings.
// Powers the dashboard's "proof it works" stats: total tokens saved, savings
// %, per-mode breakdown, and a ring buffer of recent per-request receipts.

// Receipt is one compressed request's measured outcome.
type Receipt struct {
	Timestamp        int64    `json:"timestamp"`
	Mode             string   `json:"mode"`
	OriginalTokens   int      `json:"original_tokens"`
	CompressedTokens int      `json:"compressed_tokens"`
	SavedTokens      int      `json:"saved_tokens"`
	SavingsPercent   float64  `json:"savings_percent"`
	Techniques       []string `json:"techniques"`
}

// ModeStat aggregates savings for a single mode.
type ModeStat struct {
	Requests       int     `json:"requests"`
	OriginalTokens int     `json:"original_tokens"`
	SavedTokens    int     `json:"saved_tokens"`
	SavingsPercent float64 `json:"savings_percent"`
}

// Summary is the aggregate analytics snapshot.
type Summary struct {
	TotalRequests    int                 `json:"total_requests"`
	TotalOriginal    int                 `json:"total_original_tokens"`
	TotalCompressed  int                 `json:"total_compressed_tokens"`
	TotalSaved       int                 `json:"total_saved_tokens"`
	SavingsPercent   float64             `json:"savings_percent"`
	ByMode           map[string]ModeStat `json:"by_mode"`
	Recent           []Receipt           `json:"recent"`
}

const maxRecentReceipts = 100

type tracker struct {
	mu             sync.Mutex
	totalRequests  int
	totalOriginal  int
	totalCompressed int
	byMode         map[string]*ModeStat
	recent         []Receipt
}

var globalTracker = &tracker{byMode: map[string]*ModeStat{}}

// Record logs one compression result into the global analytics tracker.
func Record(stats *Stats) {
	if stats == nil || stats.OriginalTokens <= 0 {
		return
	}
	saved := stats.OriginalTokens - stats.CompressedTokens
	if saved < 0 {
		saved = 0
	}
	r := Receipt{
		Timestamp:        stats.Timestamp,
		Mode:             string(stats.Mode),
		OriginalTokens:   stats.OriginalTokens,
		CompressedTokens: stats.CompressedTokens,
		SavedTokens:      saved,
		SavingsPercent:   stats.SavingsPercent,
		Techniques:       stats.TechniquesUsed,
	}
	if r.Timestamp == 0 {
		r.Timestamp = time.Now().Unix()
	}

	t := globalTracker
	t.mu.Lock()
	defer t.mu.Unlock()
	t.totalRequests++
	t.totalOriginal += stats.OriginalTokens
	t.totalCompressed += stats.CompressedTokens

	ms, ok := t.byMode[string(stats.Mode)]
	if !ok {
		ms = &ModeStat{}
		t.byMode[string(stats.Mode)] = ms
	}
	ms.Requests++
	ms.OriginalTokens += stats.OriginalTokens
	ms.SavedTokens += saved
	if ms.OriginalTokens > 0 {
		ms.SavingsPercent = float64(ms.SavedTokens) * 100.0 / float64(ms.OriginalTokens)
	}

	// ring buffer (newest first)
	t.recent = append([]Receipt{r}, t.recent...)
	if len(t.recent) > maxRecentReceipts {
		t.recent = t.recent[:maxRecentReceipts]
	}
}

// Analytics returns a snapshot of the aggregate compression analytics.
func Analytics() Summary {
	t := globalTracker
	t.mu.Lock()
	defer t.mu.Unlock()

	byMode := make(map[string]ModeStat, len(t.byMode))
	for k, v := range t.byMode {
		byMode[k] = *v
	}
	recent := make([]Receipt, len(t.recent))
	copy(recent, t.recent)

	saved := t.totalOriginal - t.totalCompressed
	if saved < 0 {
		saved = 0
	}
	pct := 0.0
	if t.totalOriginal > 0 {
		pct = float64(saved) * 100.0 / float64(t.totalOriginal)
	}
	return Summary{
		TotalRequests:   t.totalRequests,
		TotalOriginal:   t.totalOriginal,
		TotalCompressed: t.totalCompressed,
		TotalSaved:      saved,
		SavingsPercent:  pct,
		ByMode:          byMode,
		Recent:          recent,
	}
}
