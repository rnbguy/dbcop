//go:build cgo && unix

package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func nativeCommandAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func nativeCommandConnect(t *testing.T, address string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", "root@tcp("+address+")/?timeout=1s&readTimeout=3s&writeTimeout=3s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err = db.Ping(); err == nil {
			return db
		}
		if time.Now().After(deadline) {
			t.Fatalf("SQL listener did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeSQLCommitRollbackAndDisconnectShutdown(t *testing.T) {
	address := nativeCommandAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, address, "serializable", 42) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("serve shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serve did not drain connections")
		}
	})
	db := nativeCommandConnect(t, address)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, query := range []string{
		"CREATE DATABASE native_cli", "USE native_cli",
		"CREATE TABLE items (id INT PRIMARY KEY, v INT)",
		"INSERT INTO items VALUES (1,0),(2,0)", "CALL dbcop_seal_initial_state()",
		"BEGIN", "UPDATE items SET v=1 WHERE id=1", "SAVEPOINT keep",
		"UPDATE items SET v=2 WHERE id=1", "ROLLBACK TO SAVEPOINT keep", "COMMIT",
		"SET dbcop_read_latest=1",
	} {
		if _, err := conn.ExecContext(context.Background(), query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	var first, second int
	if err := conn.QueryRowContext(context.Background(), "SELECT v FROM items WHERE id=1").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(context.Background(), "SELECT v FROM items WHERE id=2").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("savepoint committed values = %d,%d, want 1,0", first, second)
	}
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO items VALUES (1,99)"); err == nil {
		t.Fatal("duplicate primary key accepted")
	}
	if _, err := conn.ExecContext(context.Background(), "SELECT absent FROM items"); err == nil {
		t.Fatal("unknown column accepted")
	}
	// Leave an uncommitted update for disconnect cleanup during shutdown.
	if _, err := conn.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "UPDATE items SET v=99 WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestServeRejectsOccupiedListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := serve(context.Background(), listener.Addr().String(), "causal", 1); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("occupied listener: %v", err)
	}
}

func TestMainServesSQLAndHandlesTermination(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	flag.CommandLine = flag.NewFlagSet("mockserver", flag.ContinueOnError)
	address := nativeCommandAddress(t)
	os.Args = []string{"mockserver", "--addr", address, "--iso", "serializable", "--seed", "19"}
	defer func() { flag.CommandLine, os.Args = oldFlags, oldArgs }()
	finished := make(chan struct{})
	go func() { main(); close(finished) }()
	t.Cleanup(func() {
		select {
		case <-finished:
			return
		default:
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("main listener remained running after cleanup")
		}
	})
	db := nativeCommandConnect(t, address)
	var answer int
	if err := db.QueryRow("SELECT 6 * 7").Scan(&answer); err != nil || answer != 42 {
		t.Fatalf("SQL through main = %d, %v", answer, err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("main did not handle SIGTERM")
	}
	if err := db.Ping(); err == nil {
		t.Fatal("SQL client remained usable after main shutdown")
	}
}

func TestMainReportsStartupFailure(t *testing.T) {
	if os.Getenv("DBCOP_NATIVE_MAIN_FAILURE") == "1" {
		os.Args = []string{"mockserver", "--addr", "invalid", "--iso", "invalid"}
		main()
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMainReportsStartupFailure$")
	command.Env = append(os.Environ(), "DBCOP_NATIVE_MAIN_FAILURE=1")
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("startup status = %v, output %s", err, output)
	}
	// Go's instrumented test process may also emit coverage diagnostics.
	if !strings.Contains(string(output), "mockserver: unknown isolation level \"invalid\"") {
		t.Fatalf("missing startup failure: %s", output)
	}
}
