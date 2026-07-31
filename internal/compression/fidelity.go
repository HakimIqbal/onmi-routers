package compression

// ── Fidelity gate (ported from OmniRoute fidelityGate.ts) ──────────────────
// Guards against "compression" that rewrites content for negligible gain. The
// rewriting engines (caveman/ultra/aggressive) are lossy — they change wording
// — so they should only ship when the measured token savings clear a minimum
// bar. Below that bar the risk of altering meaning isn't worth the tiny win,
// and the original body is returned untouched.
//
// OmniRoute layers this as a per-step gate inside its stacked pipeline; here it
// is applied once at the top level of Apply, which is equivalent for our
// single-mode-per-request model.

// fidelityMinSavingsPercent is the minimum measured savings a lossy mode must
// achieve to be accepted. Matches OmniRoute's default gate (3%).
const fidelityMinSavingsPercent = 3.0

// fidelityAccept reports whether a compression result with the given measured
// savings should be shipped for the mode. Lite is near-lossless and always
// accepted; lossy modes (standard/aggressive/ultra) must clear the minimum bar.
func fidelityAccept(mode Mode, savingsPercent float64) bool {
	if mode == ModeLite {
		return true
	}
	return savingsPercent >= fidelityMinSavingsPercent
}
