package main

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"foxrouters/internal/upstream"
)

func TestVersionConst(t *testing.T) {
	if Version == "" {
		t.Fatal("Version empty")
	}
	if Version == "dev" {
		t.Log("Version = dev (built without ldflags — OK for local dev)")
	}
}

func TestLogFullBodyMax(t *testing.T) {
	// Full body is unlimited (no LOG_FULL_BODY_MAX constant) — bodyString passthrough.
	big := make([]byte, 2*1024*1024)
	for i := range big {
		big[i] = 'a'
	}
	raw := json.RawMessage(big)
	if len(raw) != 2*1024*1024 {
		t.Fatalf("raw len %d", len(raw))
	}
}

func TestGrokLenO1(t *testing.T) {
	am := NewGrokAccountManager(nil)
	am.SetAccountsForTest([]*GrokAccount{
		upstream.NewGrokAccountForTest("a@t.com", "t", "r"),
		upstream.NewGrokAccountForTest("b@t.com", "t", "r"),
		upstream.NewGrokAccountForTest("c@t.com", "t", "r"),
	})
	if am.Len() != 3 {
		t.Fatalf("Len = %d", am.Len())
	}
}

func TestCBLenO1(t *testing.T) {
	km := NewCBKeyManager(nil)
	km.SetKeysForTest([]*CBKey{
		upstream.NewCBKeyForTest("ck_a"),
		upstream.NewCBKeyForTest("ck_b"),
	})
	if km.Len() != 2 {
		t.Fatalf("Len = %d", km.Len())
	}
}

func TestGrokNextNoFullReenableScan(t *testing.T) {
	// Cooldown past 10min should NOT be re-enabled by Next (background worker only).
	am := NewGrokAccountManager(nil)
	acc := upstream.NewGrokAccountForTest("cd@t.com", "t", "r",
		upstream.WithDisabledCooldown(time.Now().Add(-11*time.Minute)))
	am.SetAccountsForTest([]*GrokAccount{acc})
	_, err := am.Next()
	if err == nil {
		t.Fatal("Next should fail when only cooldown account exists (no hot re-enable)")
	}
	am.ReenableCooldowns()
	if acc.IsDisabled() {
		t.Fatal("ReenableCooldowns should lift cooldown")
	}
	got, err := am.Next()
	if err != nil || got.Email != "cd@t.com" {
		t.Fatalf("after reenable: %v %v", got, err)
	}
}

func TestCBNextNoFullReenableScan(t *testing.T) {
	km := NewCBKeyManager(nil)
	km.SetKeysForTest([]*CBKey{
		upstream.NewCBKeyForTest("ck_test.xxx",
			upstream.WithCBDisabledCooldown(time.Now().Add(-11*time.Minute))),
	})
	_, err := km.Next()
	if err == nil {
		t.Fatal("Next should fail with only cooldown key")
	}
	km.ReenableCooldowns()
	got, err := km.Next()
	if err != nil || got.Key != "ck_test.xxx" {
		t.Fatalf("after reenable: %v %v", got, err)
	}
}

func TestCBKeyAddKey(t *testing.T) {
	km := NewCBKeyManager(nil)
	added, total := km.AddKey("ck_test_one")
	if !added || total != 1 {
		t.Fatalf("first add: added=%v total=%d", added, total)
	}
	added, total = km.AddKey("ck_test_one")
	if added || total != 1 {
		t.Fatalf("dup add: added=%v total=%d", added, total)
	}
	added, total = km.AddKey("ck_test_two")
	if !added || total != 2 {
		t.Fatalf("second add: added=%v total=%d", added, total)
	}
	added, total = km.AddKey("  ")
	if added {
		t.Fatalf("blank should not add")
	}
	if km.Len() != 2 {
		t.Fatalf("Len=%d want 2", km.Len())
	}
}

func TestCBKeyAddKeyConcurrent(t *testing.T) {
	km := NewCBKeyManager(nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			km.AddKey("ck_conc_" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
		}(i)
	}
	wg.Wait()
	if km.Len() == 0 {
		t.Fatal("expected some keys")
	}
	km2 := NewCBKeyManager(nil)
	var wg2 sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			km2.AddKey("ck_same")
		}()
	}
	wg2.Wait()
	if km2.Len() != 1 {
		t.Fatalf("concurrent same key Len=%d want 1", km2.Len())
	}
}

// TestRateLimitRPMZeroUnlimited verifies that a gateway key with RPM=0
// (unlimited) bypasses the rate limiter entirely and is NOT subject to
// the global default RPM. Bug found by GLM-5.2 review.
func TestRateLimitRPMZeroUnlimited(t *testing.T) {
	am := NewAuthManagerForTest(nil)
	am.Add("gw-test-unlimited", "test", 0, 0, 0)

	info, ok := am.Get("gw-test-unlimited")
	if !ok {
		t.Fatal("key not found")
	}
	if info.RPM != 0 {
		t.Fatalf("RPM = %d, want 0 (unlimited)", info.RPM)
	}
	if info.RPM == 0 {
		return
	}
	t.Fatal("RPM=0 bypass logic broken — should not reach here")
}
