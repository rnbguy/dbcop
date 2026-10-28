package main

import (
	"math"
	"testing"
)

func TestValidateReadPolicy(t *testing.T) {
	for _, name := range []string{"uniform", "sparse"} {
		for _, probability := range []float64{0, 0.01, 1} {
			if err := validateReadPolicy(name, probability); err != nil {
				t.Errorf("validateReadPolicy(%q, %g): %v", name, probability, err)
			}
		}
		for _, probability := range []float64{-0.01, 1.01, math.NaN(), math.Inf(-1), math.Inf(1)} {
			if err := validateReadPolicy(name, probability); err == nil {
				t.Errorf("validateReadPolicy(%q, %g) accepted invalid probability", name, probability)
			}
		}
	}
	for _, name := range []string{"", "Sparse", "latest"} {
		if err := validateReadPolicy(name, 0.01); err == nil {
			t.Errorf("validateReadPolicy(%q, 0.01) accepted invalid policy", name)
		}
	}
}
