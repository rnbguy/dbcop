package main

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestServeRejectsInvalidReadPolicyBeforeOpeningListener(t *testing.T) {
	oldPolicy, oldProbability := *readPolicy, *deviationProbability
	t.Cleanup(func() {
		*readPolicy, *deviationProbability = oldPolicy, oldProbability
	})
	for _, tc := range []struct {
		name        string
		policy      string
		probability float64
		want        string
	}{
		{"unknown policy", "unknown", 0.01, "policy"},
		{"empty policy", "", 0.01, "policy"},
		{"uppercase policy", "Sparse", 0.01, "policy"},
		{"negative probability", "sparse", -0.01, "probability"},
		{"excess probability", "sparse", 1.01, "probability"},
		{"NaN probability", "sparse", math.NaN(), "probability"},
		{"positive infinite probability", "sparse", math.Inf(1), "probability"},
		{"negative infinite probability", "sparse", math.Inf(-1), "probability"},
		{"uniform invalid probability", "uniform", math.NaN(), "probability"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*readPolicy, *deviationProbability = tc.policy, tc.probability
			err := serve(context.Background(), "not-an-address", "serializable", 1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("serve returned %v, want %s validation error", err, tc.want)
			}
		})
	}
}
