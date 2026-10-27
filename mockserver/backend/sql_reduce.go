package backend

import "fmt"

// MinimizeSQLArtifact greedily reduces a verified failure in a fixed order.
// Trials count candidate executions, excluding the required strict replays.
func MinimizeSQLArtifact(artifact ReplayArtifact, maxTrials int) (ReductionResult, error) {
	if maxTrials <= 0 {
		return ReductionResult{}, fmt.Errorf("max trials must be positive")
	}
	original, err := ReplaySQLArtifact(artifact)
	if err != nil {
		return ReductionResult{}, fmt.Errorf("replay original artifact: %w", err)
	}
	best, err := CaptureSQLFailure(original.Workload, original)
	if err != nil {
		return ReductionResult{}, fmt.Errorf("recertify original artifact: %w", err)
	}
	r := sqlReduction{best: best, limit: maxTrials}
	for {
		changed := r.reduceTransactionRanges()
		if !r.exhausted {
			changed = r.reduceTransactions() || changed
		}
		if !r.exhausted {
			changed = r.reduceStatements() || changed
		}
		if !r.exhausted {
			changed = r.reduceChoices() || changed
		}
		if r.exhausted || !changed {
			break
		}
	}
	if _, err := ReplaySQLArtifact(r.best); err != nil {
		return ReductionResult{}, fmt.Errorf("replay minimized artifact: %w", err)
	}
	status := "fixed_point"
	if r.exhausted {
		status = "budget_exhausted"
	}
	return ReductionResult{Artifact: r.best, Trials: r.trials, Status: status}, nil
}

type sqlReduction struct {
	best      ReplayArtifact
	limit     int
	trials    int
	exhausted bool
}

func (r *sqlReduction) try(workload SQLWorkload, newest *ReplayChoice) bool {
	if r.trials == r.limit {
		r.exhausted = true
		return false
	}
	r.trials++
	retained := retainedSQLChoices(r.best.Choices, workload)
	result, err := runSQLReduction(workload, retained, newest)
	if err != nil || result.Failure == nil || *result.Failure != r.best.Failure {
		return false
	}
	artifact, err := CaptureSQLFailure(result.Workload, result)
	if err != nil {
		return false
	}
	r.best = artifact
	return true
}

func retainedSQLChoices(choices []ReplayChoice, workload SQLWorkload) []ReplayChoice {
	sites := make(map[[2]string]bool)
	for _, transaction := range workload.Transactions {
		for _, statement := range transaction.Statements {
			sites[[2]string{transaction.ID, statement.ID}] = true
		}
	}
	retained := make([]ReplayChoice, 0, len(choices))
	for _, choice := range choices {
		if sites[[2]string{choice.TransactionID, choice.StatementID}] {
			retained = append(retained, choice)
		}
	}
	return retained
}

func withoutSQLTransactions(workload SQLWorkload, start, end int) SQLWorkload {
	transactions := make([]SQLTransaction, 0, len(workload.Transactions)-(end-start))
	transactions = append(transactions, workload.Transactions[:start]...)
	transactions = append(transactions, workload.Transactions[end:]...)
	workload.Transactions = transactions
	return workload
}

func (r *sqlReduction) reduceTransactionRanges() bool {
	changed := false
	// Singleton complements are handled by the following individual pass.
	for partitions := 2; len(r.best.Workload.Transactions) > 1; {
		count := len(r.best.Workload.Transactions)
		removed := false
		for part := range partitions {
			start, end := part*count/partitions, (part+1)*count/partitions
			if end-start < 2 {
				continue
			}
			if r.try(withoutSQLTransactions(r.best.Workload, start, end), nil) {
				changed, removed = true, true
				partitions = max(2, partitions-1)
				break
			}
			if r.exhausted {
				return changed
			}
		}
		if removed {
			partitions = min(partitions, len(r.best.Workload.Transactions))
			continue
		}
		if partitions >= count {
			break
		}
		partitions = min(count, partitions*2)
	}
	return changed
}

func (r *sqlReduction) reduceTransactions() bool {
	changed := false
	for index := 0; index < len(r.best.Workload.Transactions); {
		if r.try(withoutSQLTransactions(r.best.Workload, index, index+1), nil) {
			changed = true
			continue
		}
		if r.exhausted {
			break
		}
		index++
	}
	return changed
}

func (r *sqlReduction) reduceStatements() bool {
	changed := false
	for transaction := range r.best.Workload.Transactions {
		for statement := 0; statement < len(r.best.Workload.Transactions[transaction].Statements); {
			workload := r.best.Workload
			workload.Transactions = append([]SQLTransaction(nil), workload.Transactions...)
			old := workload.Transactions[transaction].Statements
			statements := make([]SQLStatement, 0, len(old)-1)
			statements = append(statements, old[:statement]...)
			statements = append(statements, old[statement+1:]...)
			workload.Transactions[transaction].Statements = statements
			if r.try(workload, nil) {
				changed = true
				continue
			}
			if r.exhausted {
				return changed
			}
			statement++
		}
	}
	return changed
}

func (r *sqlReduction) reduceChoices() bool {
	changed := false
	for index := range len(r.best.Choices) {
		choice := r.best.Choices[index]
		if len(choice.Candidates) == 0 || choice.Selected == choice.Candidates[len(choice.Candidates)-1] {
			continue
		}
		if r.try(r.best.Workload, &choice) {
			changed = true
		}
		if r.exhausted {
			break
		}
	}
	return changed
}
