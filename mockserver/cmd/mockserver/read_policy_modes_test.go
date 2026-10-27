package main

import (
	"context"
	"strings"
	"testing"
)

func TestServeRejectsNegativeDeviationBudget(t *testing.T) {
	oldPolicy, oldProbability, oldBudget := *readPolicy, *deviationProbability, *deviationBudget
	t.Cleanup(func() {
		*readPolicy, *deviationProbability, *deviationBudget = oldPolicy, oldProbability, oldBudget
	})
	for _, policy := range []string{"uniform", "sparse", "age-stratified", "bounded-deviation"} {
		*readPolicy, *deviationProbability, *deviationBudget = policy, 1, -1
		if err := serve(context.Background(), "not-an-address", "serializable", 1); err == nil || !strings.Contains(err.Error(), "deviation budget must be nonnegative") {
			t.Fatalf("policy %s: negative budget reached listener: %v", policy, err)
		}
	}
}
