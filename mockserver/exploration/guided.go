package exploration

import (
	"math/rand/v2"
	"slices"
)

// Guided mutates retained traces toward novel semantic features and outcomes.
// It is a bounded heuristic search, not an exhaustive exploration verdict.
func Guided(run Runner, maxRuns, maxDecisions int, seed uint64) (Report, error) {
	report := Report{Seed: seed, Outcomes: make(map[string]int)}
	if err := validate(run, maxRuns, maxDecisions); err != nil {
		return report, err
	}
	random := rand.New(rand.NewPCG(seed, 0))
	features := make(map[Feature]bool)
	outcomes := make(map[outcomeKey]bool)
	var mutable []int
	var prefix []int
	for report.Runs < maxRuns {
		report.Runs++
		execution, err := replay(run, prefix, maxDecisions)
		if err != nil {
			report.Transactions.Add(execution.Transactions)
			report.Elapsed += execution.Elapsed
			return report, err
		}
		before := len(report.Corpus)
		record(&report, execution, features, outcomes)
		if len(report.Corpus) > before {
			if slices.ContainsFunc(execution.Decisions, func(decision Decision) bool {
				return decision.Alternatives > 1
			}) {
				mutable = append(mutable, before)
			}
		}
		if len(mutable) == 0 || report.Runs == maxRuns {
			break
		}
		decisions := report.Corpus[mutable[random.IntN(len(mutable))]].Decisions
		count := 0
		for _, decision := range decisions {
			if decision.Alternatives > 1 {
				count++
			}
		}
		selected := random.IntN(count)
		for index, decision := range decisions {
			if decision.Alternatives <= 1 {
				continue
			}
			if selected > 0 {
				selected--
				continue
			}
			prefix = prefix[:0]
			for i := range index + 1 {
				prefix = append(prefix, decisions[i].Choice)
			}
			choice := random.IntN(decision.Alternatives - 1)
			if choice >= decision.Choice {
				choice++
			}
			prefix[index] = choice
			break
		}
	}
	return report, nil
}
