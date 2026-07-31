package eval

import "foxrouters/internal/compression"

// aggregate builds the final EvalReport from per-case records (ported from
// OmniRoute eval/aggregate.ts). Errored cases are counted but excluded from
// fidelity/ratio averages.
func aggregate(opts RunEvalOptions, records []EvalRecord, partial bool, totalCostUsd float64) *EvalReport {
	scored := 0
	errored := 0
	fidelitySame := 0
	ratioSum := 0.0

	byKind := map[ContentKind]*struct {
		scored      int
		fidelitySame int
		ratioSum    float64
		goldFull    int
		goldComp    int
		goldCases   int
	}{}

	for i := range records {
		r := &records[i]
		if r.Errored {
			errored++
			continue
		}
		scored++
		if r.Fidelity == compression.VerdictSame {
			fidelitySame++
		}
		ratioSum += r.Savings.Ratio

		k := byKind[r.Kind]
		if k == nil {
			k = &struct {
				scored       int
				fidelitySame int
				ratioSum     float64
				goldFull     int
				goldComp     int
				goldCases    int
			}{}
			byKind[r.Kind] = k
		}
		k.scored++
		if r.Fidelity == compression.VerdictSame {
			k.fidelitySame++
		}
		k.ratioSum += r.Savings.Ratio
		if r.GoldFull != nil && r.GoldCompressed != nil {
			k.goldCases++
			if *r.GoldFull {
				k.goldFull++
			}
			if *r.GoldCompressed {
				k.goldComp++
			}
		}
	}

	fidelityPct := 0.0
	if scored > 0 {
		fidelityPct = float64(fidelitySame) * 100.0 / float64(scored)
	}
	meanRatio := 1.0
	if scored > 0 {
		meanRatio = ratioSum / float64(scored)
	}

	kindSummaries := make([]KindSummary, 0, len(byKind))
	for kind, k := range byKind {
		ks := KindSummary{Kind: kind, CasesScored: k.scored}
		if k.scored > 0 {
			ks.FidelityPreservedPct = float64(k.fidelitySame) * 100.0 / float64(k.scored)
			ks.MeanRatio = k.ratioSum / float64(k.scored)
		}
		if k.goldCases > 0 {
			fullAcc := float64(k.goldFull) * 100.0 / float64(k.goldCases)
			compAcc := float64(k.goldComp) * 100.0 / float64(k.goldCases)
			delta := compAcc - fullAcc
			ks.GoldAccuracyDeltaPct = &delta
		}
		kindSummaries = append(kindSummaries, ks)
	}

	return &EvalReport{
		Stamps: RunStamps{
			AnswerModel: opts.AnswerModel,
			JudgeModel:  opts.JudgeModel,
			Mode:        string(opts.Mode),
			SampleSize:  len(records),
		},
		Partial:              partial,
		TotalCostUsd:         totalCostUsd,
		CasesScored:          scored,
		CasesErrored:         errored,
		FidelityPreservedPct: fidelityPct,
		MeanRatio:            meanRatio,
		ByKind:               kindSummaries,
		Records:              records,
	}
}
