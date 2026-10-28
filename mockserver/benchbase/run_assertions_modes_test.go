package main

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewReadPoliciesValidate(t *testing.T) {
	for _, policy := range []string{"age-stratified", "bounded-deviation"} {
		for _, probability := range []float64{0, 0.01, 1} {
			if err := validateReadPolicy(policy, probability); err != nil {
				t.Fatalf("policy %s probability %g: %v", policy, probability, err)
			}
		}
		for _, probability := range []float64{-1, 2, math.NaN(), math.Inf(1)} {
			if err := validateReadPolicy(policy, probability); err == nil {
				t.Fatalf("policy %s accepted probability %g", policy, probability)
			}
		}
	}
}

func TestNewReadPolicyFlagsReachServerAndOutcome(t *testing.T) {
	oldPolicy, oldProbability, oldBudget := *readPolicy, *deviationProbability, *deviationBudget
	t.Cleanup(func() {
		*readPolicy, *deviationProbability, *deviationBudget = oldPolicy, oldProbability, oldBudget
	})
	for _, policy := range []string{"age-stratified", "bounded-deviation"} {
		t.Run(policy, func(t *testing.T) {
			root := t.TempDir()
			argsPath := filepath.Join(root, "args")
			t.Setenv("DBCOP_POLICY_ARGS", argsPath)
			server := filepath.Join(root, "server")
			if err := os.WriteFile(server, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DBCOP_POLICY_ARGS\"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(root, "input.xml")
			if err := os.WriteFile(config, []byte(`<parameters><url>jdbc:mysql://localhost/voter</url><randomSeed>1</randomSeed></parameters>`), 0600); err != nil {
				t.Fatal(err)
			}
			*readPolicy, *deviationProbability, *deviationBudget = policy, 1, 2
			got := experiment(server, "", "", "", config, root, "voter", "serializable", "127.0.0.1:0", 42, time.Second)
			if got.Phase != "start" || !strings.Contains(got.Error, "server exited") || got.ReadPolicy != policy || got.DeviationProbability != 1 || got.DeviationBudget != 2 || got.Seed != 42 {
				t.Fatalf("incomplete outcome lost policy configuration: %+v", got)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"--read-policy\n" + policy + "\n", "--deviation-probability\n1\n", "--deviation-budget\n2\n", "--seed\n42\n"} {
				if !strings.Contains(string(args), want) {
					t.Fatalf("server arguments %q lack %q", args, want)
				}
			}
			encoded, err := json.Marshal(got)
			if err != nil || !strings.Contains(string(encoded), `"deviation_budget":2`) {
				t.Fatalf("JSON outcome lost budget: %s, %v", encoded, err)
			}
		})
	}
}

func TestNewReadPolicyRunnerProcess(t *testing.T) {
	if os.Getenv("DBCOP_TEST_NEW_POLICY_RUNNER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("assertion-runner", flag.ExitOnError)
	flag.CommandLine.StringVar(readPolicy, "read-policy", "uniform", "")
	flag.CommandLine.Float64Var(deviationProbability, "deviation-probability", 0.01, "")
	flag.CommandLine.IntVar(deviationBudget, "deviation-budget", 1, "")
	main()
	os.Exit(0)
}

func TestRunnerRejectsNegativeDeviationBudget(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBCOP_TEST_NEW_POLICY_RUNNER", "1")
	command := exec.Command(binary, "-test.run=^TestNewReadPolicyRunnerProcess$", "--",
		"--server", "unused", "--assertions", "unused", "--client-dir", "unused", "--config", "unused",
		"--output-dir", filepath.Join(t.TempDir(), "out"), "--benchmark", "voter", "--level", "serializable",
		"--read-policy", "bounded-deviation", "--deviation-budget", "-1")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "deviation-budget must be nonnegative") {
		t.Fatalf("negative budget was not rejected: %s, %v", output, err)
	}
}
