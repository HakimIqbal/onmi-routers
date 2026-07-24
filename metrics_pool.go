// metrics_pool.go — snapshots pool sizes into internal/metrics gauges.
// The lookup uses only exported APIs on the managers (GetAll / Stats /
// IsDisabled), so this file can live in package main even though the
// managers themselves are in internal/upstream + internal/auth.
package main

import (
	"foxrouters/internal/metrics"
)

// updatePoolGauges snapshots pool sizes into active/disabled gauges.
// Called every 10s from a background ticker — cheap RLock walk, no
// hot-path overhead.
func updatePoolGauges(grokAM *GrokAccountManager, cbKM *CBKeyManager, authMgr *AuthManager, cfKM *CFKeyManager) {
	if grokAM != nil {
		var gActive, gDisabled int
		for _, a := range grokAM.GetAll() {
			if a.IsDisabled() {
				gDisabled++
			} else {
				gActive++
			}
		}
		metrics.SetPoolGauges("grok", gActive, gDisabled)
	}
	if cbKM != nil {
		var cActive, cDisabled int
		for _, k := range cbKM.GetAll() {
			_, _, disabled := k.Stats()
			if disabled {
				cDisabled++
			} else {
				cActive++
			}
		}
		metrics.SetPoolGauges("codebuddy", cActive, cDisabled)
	}
	if cfKM != nil {
		var cfActive, cfDisabled int
		for _, k := range cfKM.GetAll() {
			_, _, disabled := k.Stats()
			if disabled {
				cfDisabled++
			} else {
				cfActive++
			}
		}
		metrics.SetPoolGauges("cloudflare", cfActive, cfDisabled)
	}
	if authMgr != nil {
		var aActive, aDisabled int
		for _, info := range authMgr.GetAll() {
			if info.Disabled {
				aDisabled++
			} else {
				aActive++
			}
		}
		metrics.SetPoolGauges("auth", aActive, aDisabled)
	}
}
