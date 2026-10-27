// Command assertions evaluates the MonkeyDB paper's application invariants over MySQL.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type Assertion struct {
	ID    string `json:"id"`
	Holds bool   `json:"holds"`
}

func check(ctx context.Context, dsn, benchmark string) ([]Assertion, error) {
	if benchmark != "tpcc" && benchmark != "smallbank" && benchmark != "voter" && benchmark != "wikipedia" {
		return nil, fmt.Errorf("unknown benchmark %q", benchmark)
	}
	if dsn == "" {
		return nil, errors.New("--dsn is required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Validation follows the artifact's latest-admitted read strategy, not weak random selection.
	if _, err := conn.ExecContext(ctx, "SET @@session.dbcop_read_latest = 1"); err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var assertions []Assertion
	if benchmark == "tpcc" {
		assertions, err = checkTPCC(ctx, tx)
	} else {
		assertions, err = checkApplications(ctx, tx, benchmark)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	return assertions, nil
}

func main() {
	dsn := flag.String("dsn", "", "MySQL DSN including the application database")
	benchmark := flag.String("benchmark", "", "tpcc, smallbank, voter, or wikipedia")
	timeout := flag.Duration("timeout", 5*time.Minute, "assertion evaluation deadline")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	assertions, err := check(ctx, *dsn, *benchmark)
	result := struct {
		Benchmark  string      `json:"benchmark"`
		Assertions []Assertion `json:"assertions"`
		Error      string      `json:"error,omitempty"`
	}{Benchmark: *benchmark, Assertions: assertions}
	if err != nil {
		result.Error = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		fmt.Fprintln(os.Stderr, encodeErr)
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}
