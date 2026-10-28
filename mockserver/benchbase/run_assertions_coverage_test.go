package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

func TestReadMeasurementRequiresCompleteStockSummary(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
		valid bool
	}{
		{"complete", map[string]string{"run.summary.json": `{"Elapsed Time (nanoseconds)":10000000000,"Measured Requests":87,"Final State":"EXIT","other stock field":42}`}, true},
		{"missing", nil, false},
		{"multiple", map[string]string{"one.summary.json": "{}", "two.summary.json": "{}"}, false},
		{"malformed", map[string]string{"run.summary.json": "{"}, false},
		{"trailing", map[string]string{"run.summary.json": `{} {}`}, false},
		{"unfinished", map[string]string{"run.summary.json": `{"Elapsed Time (nanoseconds)":100,"Measured Requests":2,"Final State":"MEASURE"}`}, false},
		{"negative requests", map[string]string{"run.summary.json": `{"Elapsed Time (nanoseconds)":100,"Measured Requests":-1,"Final State":"EXIT"}`}, false},
		{"zero duration", map[string]string{"run.summary.json": `{"Elapsed Time (nanoseconds)":0,"Measured Requests":0,"Final State":"EXIT"}`}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range test.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			measured, err := readMeasurement(dir)
			if (err == nil) != test.valid {
				t.Fatalf("measurement=%#v error=%v valid=%t", measured, err, test.valid)
			}
			if test.valid && (measured.ElapsedNanoseconds != 10000000000 || measured.MeasuredRequests != 87) {
				t.Fatalf("wrong stock duration/request count: %#v", measured)
			}
		})
	}
}

func TestValidateCoverageRejectsCorruptFeatureEvidence(t *testing.T) {
	base := exploration.HistoryCoverage{Transactions: 1, Reads: 1, Features: []exploration.FeatureObservation{{
		Feature: exploration.HistoryFeature{Reader: 1, Initial: true}, FirstTransaction: 1,
	}}}
	for _, test := range []struct {
		name   string
		mutate func(*exploration.HistoryCoverage)
	}{
		{"negative transactions", func(c *exploration.HistoryCoverage) { c.Transactions = -1 }},
		{"negative reads", func(c *exploration.HistoryCoverage) { c.Reads = -1 }},
		{"missing features", func(c *exploration.HistoryCoverage) { c.Features = nil }},
		{"more features than reads", func(c *exploration.HistoryCoverage) { c.Reads = 0 }},
		{"reads without transaction", func(c *exploration.HistoryCoverage) { c.Transactions = 0 }},
		{"future discovery", func(c *exploration.HistoryCoverage) { c.Features[0].FirstTransaction = 2 }},
		{"zero discovery", func(c *exploration.HistoryCoverage) { c.Features[0].FirstTransaction = 0 }},
		{"zero reader", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.Reader = 0 }},
		{"negative writer", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.Writer = -1 }},
		{"negative previous writer", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.PreviousWriter = -1 }},
		{"false root flag", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.Initial = false }},
		{"previous writer without read", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.PreviousWriter = 2 }},
		{"variable without previous read", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.SameVariable = true }},
		{"source without previous read", func(c *exploration.HistoryCoverage) { c.Features[0].Feature.SameSource = true }},
		{"same source different writers", func(c *exploration.HistoryCoverage) {
			c.Features[0].Feature.HasPreviousRead = true
			c.Features[0].Feature.SameSource = true
			c.Features[0].Feature.PreviousWriter = 2
		}},
		{"duplicate feature", func(c *exploration.HistoryCoverage) { c.Reads = 2; c.Features = append(c.Features, c.Features[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := base
			c.Features = append([]exploration.FeatureObservation(nil), base.Features...)
			test.mutate(&c)
			if err := validateCoverage(c); err == nil {
				t.Fatalf("accepted corrupt evidence: %#v", c)
			}
		})
	}
	if err := validateCoverage(base); err != nil {
		t.Fatal(err)
	}
	if err := validateCoverage(exploration.HistoryCoverage{Features: []exploration.FeatureObservation{}}); err != nil {
		t.Fatal(err)
	}
}
