package exploration

// Systematic explores every admitted read-choice leaf within the run and decision bounds.
// Complete is true only when no unexplored branch remains.
func Systematic(run Runner, maxRuns, maxDecisions int) (Report, error) {
	report := Report{Outcomes: make(map[string]int)}
	if err := validate(run, maxRuns, maxDecisions); err != nil {
		return report, err
	}
	features := make(map[Feature]bool)
	outcomes := make(map[outcomeKey]bool)
	var visit func([]int) (bool, error)
	visit = func(prefix []int) (bool, error) {
		report.Runs++
		execution, err := replay(run, prefix, maxDecisions)
		if err != nil {
			report.Transactions.Add(execution.Transactions)
			report.Elapsed += execution.Elapsed
			return false, err
		}
		record(&report, execution, features, outcomes)
		for i := len(prefix); i < len(execution.Decisions); i++ {
			for alternative := range execution.Decisions[i].Alternatives - 1 {
				if report.Runs == maxRuns {
					return false, nil
				}
				next := make([]int, i+1)
				for j := range i {
					next[j] = execution.Decisions[j].Choice
				}
				next[i] = alternative
				complete, err := visit(next)
				if err != nil || !complete {
					return false, err
				}
			}
		}
		return true, nil
	}
	var err error
	report.Complete, err = visit(nil)
	return report, err
}
