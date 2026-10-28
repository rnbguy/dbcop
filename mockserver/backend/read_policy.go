package backend

import (
	"fmt"
	"math"
)

// SetReadPolicy configures selection among admitted versions, not admission.
// Sparse and bounded-deviation reads prefer newest; age-stratified balances ages.
func (s *Store) SetReadPolicy(name string, deviationProbability float64) error {
	if name != "uniform" && name != "sparse" && name != "age-stratified" && name != "bounded-deviation" {
		return fmt.Errorf("unknown read policy %q", name)
	}
	if math.IsNaN(deviationProbability) || math.IsInf(deviationProbability, 0) || deviationProbability < 0 || deviationProbability > 1 {
		return fmt.Errorf("deviation probability must be finite and in [0, 1]")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readPolicy = name
	s.deviationProbability = deviationProbability
	return nil
}

// SetDeviationBudget caps older-source choices per bounded-deviation transaction.
func (s *Store) SetDeviationBudget(budget int) error {
	if budget < 0 {
		return fmt.Errorf("deviation budget must be nonnegative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviationBudget = budget
	return nil
}

// selectReadIndex receives admitted sources in oldest-to-newest order.
// hasInitial describes provenance, not merely the first candidate's position.
func (s *Store) selectReadIndex(n int, hasInitial bool, txn *Txn) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.weakRandom {
		return 0, false
	}
	switch s.readPolicy {
	case "age-stratified":
		// At most four disjoint, nonempty buckets; no per-read allocation.
		var starts, widths [4]int
		buckets, first := 0, 0
		if hasInitial {
			starts[0], widths[0] = 0, 1
			buckets, first = 1, 1
		}
		if first < n {
			starts[buckets], widths[buckets] = first, 1
			buckets++
		}
		if first+1 < n {
			starts[buckets], widths[buckets] = n-1, 1
			buckets++
		}
		if first+2 < n {
			starts[buckets], widths[buckets] = first+1, n-first-2
			buckets++
		}
		bucket := s.rng.Intn(buckets)
		index := starts[bucket]
		if widths[bucket] > 1 {
			index += s.rng.Intn(widths[bucket])
		}
		return index, false
	case "bounded-deviation":
		if n == 1 {
			return 0, false
		}
		deviations := 0
		if txn != nil {
			txn.mu.Lock()
			deviations = txn.deviations
			txn.mu.Unlock()
		}
		if deviations >= s.deviationBudget {
			return n - 1, false
		}
		fallthrough
	case "sparse":
		if n > 1 && s.rng.Float64() < s.deviationProbability {
			return s.rng.Intn(n - 1), s.readPolicy == "bounded-deviation"
		}
		return n - 1, false
	default:
		return s.rng.Intn(n), false
	}
}
