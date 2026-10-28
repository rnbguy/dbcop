package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasurementUsesLiteralOutputDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "policy[seed42]")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.summary.json"), []byte(`{"Elapsed Time (nanoseconds)":10000000000,"Measured Requests":23,"Final State":"EXIT"}`), 0600); err != nil {
		t.Fatal(err)
	}
	measured, err := readMeasurement(dir)
	if err != nil || measured.MeasuredRequests != 23 || measured.ElapsedNanoseconds != 10000000000 {
		t.Fatalf("valid literal directory was not measured: result=%+v error=%v", measured, err)
	}
}

func TestMeasurementDoesNotTreatDirectoryAsSummary(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "run.summary.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if measured, err := readMeasurement(dir); err == nil || measured != nil {
		t.Fatalf("summary directory became completed measurement: result=%+v error=%v", measured, err)
	}
}

func TestMeasurementPreservesMissingOutputError(t *testing.T) {
	measured, err := readMeasurement(filepath.Join(t.TempDir(), "missing-output"))
	if !os.IsNotExist(err) || measured != nil {
		t.Fatalf("missing output became a measurement or lost its filesystem cause: result=%+v error=%v", measured, err)
	}
}
