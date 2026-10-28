// Run with: go run ./benchbase/run_assertions.go --help
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

var (
	readPolicy           = flag.String("read-policy", "uniform", "read selection policy: uniform|sparse|age-stratified|bounded-deviation")
	deviationProbability = flag.Float64("deviation-probability", 0.01, "probability of an older admitted source under sparse or bounded-deviation")
	deviationBudget      = flag.Int("deviation-budget", 1, "maximum older-source deviations per bounded-deviation SQL transaction")
	collectCoverage      = flag.Bool("collect-coverage", false, "capture committed semantic features before assertions and stock BenchBase measurement duration")
)

type assertion struct {
	ID    string `json:"id"`
	Holds bool   `json:"holds"`
}
type checkResult struct {
	Benchmark  string      `json:"benchmark"`
	Assertions []assertion `json:"assertions"`
	Error      string      `json:"error,omitempty"`
}

type measurement struct {
	ElapsedNanoseconds int64  `json:"Elapsed Time (nanoseconds)"`
	MeasuredRequests   int    `json:"Measured Requests"`
	FinalState         string `json:"Final State"`
}
type outcome struct {
	Benchmark            string                       `json:"benchmark"`
	Level                string                       `json:"level"`
	Seed                 uint64                       `json:"seed"`
	ReadPolicy           string                       `json:"read_policy"`
	DeviationProbability float64                      `json:"deviation_probability"`
	DeviationBudget      int                          `json:"deviation_budget"`
	Phase                string                       `json:"phase"`
	Error                string                       `json:"error,omitempty"`
	Baseline             checkResult                  `json:"baseline"`
	Final                checkResult                  `json:"final"`
	Histograms           map[string]int               `json:"histograms,omitempty"`
	Coverage             *exploration.HistoryCoverage `json:"coverage,omitempty"`
	Measurement          *measurement                 `json:"measurement,omitempty"`
}

func validateReadPolicy(name string, probability float64) error {
	if name != "uniform" && name != "sparse" && name != "age-stratified" && name != "bounded-deviation" {
		return fmt.Errorf("read-policy must be uniform or sparse or age-stratified or bounded-deviation")
	}
	if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
		return fmt.Errorf("deviation-probability must be finite and in [0,1]")
	}
	return nil
}

func replaceElement(data, name, value string) (string, error) {
	before, rest, ok := strings.Cut(data, "<"+name+">")
	if !ok {
		return "", fmt.Errorf("missing config element %s", name)
	}
	_, after, ok := strings.Cut(rest, "</"+name+">")
	if !ok {
		return "", fmt.Errorf("unclosed config element %s", name)
	}
	return before + "<" + name + ">" + value + "</" + name + ">" + after, nil
}

func command(dir, log string, timeout time.Duration, env []string, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	data, err := cmd.CombinedOutput()
	if writeErr := os.WriteFile(log, data, 0600); writeErr != nil {
		return nil, errors.Join(err, ctx.Err(), writeErr)
	}
	return data, errors.Join(err, ctx.Err())
}

func checkAssertions(out, phase string, timeout time.Duration, benchmark, binary string, args ...string) (checkResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	err = errors.Join(err, ctx.Err(),
		os.WriteFile(filepath.Join(out, phase+".json"), data, 0600),
		os.WriteFile(filepath.Join(out, phase+".stderr.log"), stderr.Bytes(), 0600))
	var checked checkResult
	if jsonErr := json.Unmarshal(data, &checked); jsonErr != nil {
		return checked, errors.Join(err, jsonErr)
	}
	if checked.Error != "" {
		return checked, errors.Join(err, errors.New(checked.Error))
	}
	expected := 1
	if benchmark == "tpcc" {
		expected = 12
	} else if benchmark == "wikipedia" {
		expected = 3
	}
	if len(checked.Assertions) != expected {
		return checked, errors.Join(err, errors.New("incomplete assertion set"))
	}
	return checked, err
}

func parseHistograms(text string) (map[string]int, error) {
	labels := []string{"Completed Transactions", "Aborted Transactions", "Rejected Transactions (Server Retry)", "Rejected Transactions (Retry Different)", "Unexpected SQL Errors", "Unknown Status Transactions"}
	counts := make(map[string]int, len(labels))
	for i, label := range labels {
		start := strings.Index(text, label+":")
		if start < 0 {
			return nil, fmt.Errorf("missing workload histogram %s", label)
		}
		section := text[start+len(label)+1:]
		if i+1 < len(labels) {
			end := strings.Index(section, labels[i+1]+":")
			if end < 0 {
				return nil, fmt.Errorf("missing workload histogram %s", labels[i+1])
			}
			section = section[:end]
		}
		found := strings.Contains(section, "<EMPTY>")
		for _, line := range strings.Split(section, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "com.oltpbenchmark.") {
				continue
			}
			_, rest, ok := strings.Cut(line, "[")
			if !ok {
				return nil, fmt.Errorf("malformed %s row", label)
			}
			raw, _, ok := strings.Cut(rest, "]")
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if !ok || err != nil || n < 0 {
				return nil, fmt.Errorf("invalid %s count %q", label, raw)
			}
			found = true
			counts[label] += n
		}
		if !found {
			return nil, fmt.Errorf("empty or malformed workload histogram %s", label)
		}
	}
	return counts, nil
}

func readMeasurement(directory string) (*measurement, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	name, matches := "", 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".summary.json") {
			name, matches = entry.Name(), matches+1
		}
	}
	if matches != 1 {
		return nil, fmt.Errorf("expected one BenchBase measurement summary, got %d", matches)
	}
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return nil, err
	}
	var measured measurement
	if err := json.Unmarshal(data, &measured); err != nil {
		return nil, fmt.Errorf("decode BenchBase measurement: %w", err)
	}
	if measured.ElapsedNanoseconds <= 0 || measured.MeasuredRequests < 0 || measured.FinalState != "EXIT" {
		return nil, errors.New("incomplete or invalid BenchBase measurement")
	}
	return &measured, nil
}

func validateCoverage(coverage exploration.HistoryCoverage) error {
	if coverage.Transactions < 0 || coverage.Reads < 0 || coverage.Features == nil || len(coverage.Features) > coverage.Reads || (coverage.Transactions == 0 && coverage.Reads != 0) {
		return errors.New("invalid coverage counts")
	}
	seen := make(map[exploration.HistoryFeature]bool, len(coverage.Features))
	for _, observation := range coverage.Features {
		f := observation.Feature
		if observation.FirstTransaction <= 0 || observation.FirstTransaction > coverage.Transactions || f.Reader <= 0 || f.Writer < 0 || f.PreviousWriter < 0 || f.Initial != (f.Writer == 0) || (!f.HasPreviousRead && (f.PreviousWriter != 0 || f.SameSource || f.SameVariable)) || (f.SameSource && f.Writer != f.PreviousWriter) || seen[f] {
			return errors.New("invalid or duplicate coverage feature")
		}
		seen[f] = true
	}
	return nil
}

func experiment(server, checker, client, java, config, out, benchmark, level, address string, seed uint64, timeout time.Duration) (result outcome) {
	result.Benchmark = benchmark
	result.Level = level
	result.Seed = seed
	result.ReadPolicy = *readPolicy
	result.DeviationProbability = *deviationProbability
	result.DeviationBudget = *deviationBudget
	result.Phase = "config"
	fail := func(err error) outcome { result.Error = err.Error(); return result }
	data, err := os.ReadFile(config)
	if err != nil {
		return fail(err)
	}
	var settings struct {
		URL      string `xml:"url"`
		User     string `xml:"username"`
		Password string `xml:"password"`
	}
	if err := xml.Unmarshal(data, &settings); err != nil {
		return fail(err)
	}
	endpoint, err := url.Parse(strings.TrimPrefix(settings.URL, "jdbc:"))
	if err != nil {
		return fail(err)
	}
	if strings.Trim(endpoint.Path, "/") != benchmark {
		return fail(errors.New("config database must match benchmark"))
	}
	endpoint.Host = address
	text, err := replaceElement(string(data), "url", strings.ReplaceAll("jdbc:"+endpoint.String(), "&", "&amp;"))
	if err != nil {
		return fail(err)
	}
	text, err = replaceElement(text, "randomSeed", strconv.FormatUint(seed, 10))
	if err != nil {
		return fail(err)
	}
	configPath := filepath.Join(out, "config.xml")
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		return fail(err)
	}
	result.Phase = "start"
	serverLog, err := os.Create(filepath.Join(out, "server.log"))
	if err != nil {
		return fail(err)
	}
	defer serverLog.Close()
	process := exec.Command(server, "--addr", address, "--iso", level, "--seed", strconv.FormatUint(seed, 10),
		"--read-policy", *readPolicy, "--deviation-probability", strconv.FormatFloat(*deviationProbability, 'g', -1, 64),
		"--deviation-budget", strconv.Itoa(*deviationBudget))
	process.Stdout, process.Stderr = serverLog, serverLog
	if err := process.Start(); err != nil {
		return fail(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	defer func() {
		_ = process.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = process.Process.Kill()
			<-done
		}
	}()
	ready := false
	for attempt := 0; attempt < 100; attempt++ {
		conn, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			ready = true
			break
		}
		select {
		case processErr := <-done:
			done <- processErr
			return fail(fmt.Errorf("server exited: %v", processErr))
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		return fail(errors.New("server readiness timeout"))
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fail(err)
	}
	sqlCommand := func(phase, query string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "mysql", "--host="+host, "--port="+port, "--user="+settings.User, "--batch", "--skip-column-names", "--execute", query)
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+settings.Password)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		data, err := cmd.Output()
		err = errors.Join(err, ctx.Err(),
			os.WriteFile(filepath.Join(out, phase+".log"), data, 0600),
			os.WriteFile(filepath.Join(out, phase+".stderr.log"), stderr.Bytes(), 0600))
		return data, err
	}
	result.Phase = "create"
	if _, err := sqlCommand("create", "CREATE DATABASE `"+benchmark+"`"); err != nil {
		return fail(err)
	}
	runClient := func(phase string, load bool) error {
		data, err := command(client, filepath.Join(out, phase+".log"), timeout, nil, java, "-jar", "benchbase.jar", "-b", benchmark, "-c", configPath, "--create="+strconv.FormatBool(load), "--load="+strconv.FormatBool(load), "--execute="+strconv.FormatBool(!load), "-d", filepath.Join(out, phase+"-results"))
		if err == nil && !load {
			result.Histograms, err = parseHistograms(string(data))
		}
		if err == nil && !load && *collectCoverage {
			result.Measurement, err = readMeasurement(filepath.Join(out, phase+"-results"))
		}
		return err
	}
	result.Phase = "load"
	if err := runClient("load", true); err != nil {
		return fail(err)
	}
	result.Phase = "normalize"
	if benchmark == "smallbank" {
		if _, err := sqlCommand("normalize", "USE smallbank; UPDATE savings SET bal=2500; UPDATE checking SET bal=2500;"); err != nil {
			return fail(err)
		}
	}
	if benchmark == "wikipedia" {
		output, err := sqlCommand("normalization-precondition", "SELECT (SELECT COUNT(*) FROM wikipedia.recentchanges) + (SELECT COUNT(*) FROM wikipedia.logging)")
		if err != nil {
			return fail(err)
		}
		if strings.TrimSpace(string(output)) != "0" {
			return fail(errors.New("Wikipedia load includes history; refusing to reset edit counts"))
		}
		if _, err := sqlCommand("normalize", "UPDATE wikipedia.useracct SET user_editcount=0"); err != nil {
			return fail(err)
		}
	}
	mysqlConfig := mysql.NewConfig()
	mysqlConfig.User, mysqlConfig.Passwd, mysqlConfig.Net, mysqlConfig.Addr, mysqlConfig.DBName = settings.User, settings.Password, "tcp", address, benchmark
	check := func(phase string) (checkResult, error) {
		return checkAssertions(out, phase, timeout, benchmark, checker, "--dsn", mysqlConfig.FormatDSN(), "--benchmark", benchmark, "--timeout", timeout.String())
	}
	result.Phase = "baseline"
	result.Baseline, err = check("baseline")
	if err != nil {
		return fail(err)
	}
	for _, a := range result.Baseline.Assertions {
		if !a.Holds {
			return fail(fmt.Errorf("baseline violates %s", a.ID))
		}
	}
	result.Phase = "seal"
	if _, err := sqlCommand("seal", "USE `"+benchmark+"`; CALL dbcop_seal_initial_state()"); err != nil {
		return fail(err)
	}
	result.Phase = "execute"
	if err := runClient("execute", false); err != nil {
		return fail(err)
	}
	if *collectCoverage {
		result.Phase = "coverage"
		data, captureErr := sqlCommand("coverage", "USE `"+benchmark+"`; CALL dbcop_coverage()")
		if captureErr != nil {
			return fail(captureErr)
		}
		var coverage exploration.HistoryCoverage
		if err := json.Unmarshal(bytes.TrimSpace(data), &coverage); err != nil {
			return fail(fmt.Errorf("decode coverage: %w", err))
		}
		if err := validateCoverage(coverage); err != nil {
			return fail(err)
		}
		result.Coverage = &coverage
	}
	result.Phase = "assertions"
	result.Final, err = check("assertions")
	if err != nil {
		return fail(err)
	}
	if errors := result.Histograms["Unexpected SQL Errors"] + result.Histograms["Unknown Status Transactions"]; errors != 0 {
		result.Phase = "workload-error"
		return fail(fmt.Errorf("workload recorded %d unexpected SQL errors or unknown outcomes; assertions retained separately", errors))
	}
	result.Phase = "complete"
	return result
}

func main() {
	server := flag.String("server", "", "optimized mockserver executable")
	checker := flag.String("assertions", "", "assertions executable")
	client := flag.String("client-dir", "", "BenchBase MySQL distribution directory")
	java := flag.String("java", "java", "Java executable")
	config := flag.String("config", "", "standard BenchBase XML configuration")
	out := flag.String("output-dir", "", "new output directory")
	benchmark := flag.String("benchmark", "", "tpcc, smallbank, voter, or wikipedia")
	level := flag.String("level", "", "server isolation level")
	address := flag.String("addr", "127.0.0.1:13307", "exclusive local server address")
	runs := flag.Int("runs", 10, "independent fresh-state experiments")
	seed := flag.Uint64("seed", 1, "first seed; each independent experiment increments it")
	timeout := flag.Duration("timeout", 5*time.Minute, "deadline per phase")
	flag.Parse()
	if *server == "" || *checker == "" || *client == "" || *config == "" || *out == "" || *level == "" || *runs <= 0 || (*benchmark != "tpcc" && *benchmark != "smallbank" && *benchmark != "voter" && *benchmark != "wikipedia") {
		fmt.Fprintln(os.Stderr, "required: --server --assertions --client-dir --config --output-dir --benchmark --level; --runs must be positive")
		os.Exit(1)
	}
	if err := validateReadPolicy(*readPolicy, *deviationProbability); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *deviationBudget < 0 {
		fmt.Fprintln(os.Stderr, "deviation-budget must be nonnegative")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "read-policy=%s deviation-probability=%g deviation-budget=%d\n", *readPolicy, *deviationProbability, *deviationBudget)
	absoluteOut, err := filepath.Abs(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Mkdir(absoluteOut, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	failed := false
	for i := 0; i < *runs; i++ {
		chosen := *seed + uint64(i)
		dir := filepath.Join(absoluteOut, fmt.Sprintf("run-%03d-seed-%d", i+1, chosen))
		if err := os.Mkdir(dir, 0700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		result := experiment(*server, *checker, *client, *java, *config, dir, *benchmark, *level, *address, chosen, *timeout)
		data, err := json.MarshalIndent(result, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "outcome.json"), data, 0600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if result.Error != "" {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
