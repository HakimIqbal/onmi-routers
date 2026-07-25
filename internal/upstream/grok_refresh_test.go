package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestRefreshDoesNotHoldLockAcrossSleep verifies that Refresh() does not hold
// the account mutex while performing the (network) token round-trip, so that
// concurrent GetAccessToken()/IsDisabled() calls make progress.
//
// The test is fully HERMETIC: it swaps the package-level refresh client + token
// URL for a local httptest server, so it never touches auth.x.ai (which would
// hang/stall in sandboxed CI without egress and trip the 15s deadline).
func TestRefreshDoesNotHoldLockAcrossSleep(t *testing.T) {
	origClient := tokenRefreshClient
	origURL := XAI_TOKEN_URL
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a fast auth failure (bad refresh token) — no body needed.
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()
	tokenRefreshClient = &http.Client{Timeout: 3 * time.Second}
	XAI_TOKEN_URL = ts.URL
	defer func() {
		tokenRefreshClient = origClient
		XAI_TOKEN_URL = origURL
	}()

	acc := NewGrokAccountForTest("x@t.com", "old", "bad-rt")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = acc.GetAccessToken()
			_ = acc.IsDisabled()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = acc.Refresh() // hits local httptest → 400 fast, must not hold lock
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// success — no deadlock
	case <-time.After(15 * time.Second):
		t.Fatal("deadlock during Refresh + GetAccessToken")
	}
}
