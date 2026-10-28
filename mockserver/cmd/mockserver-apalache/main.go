// Command mockserver-apalache serves the shared MySQL engine using an operator-managed
// Apalache v0.62.4 explorer. Provision the pinned artifact in isolation or behind a
// firewall: a loopback client URL does not restrict the explorer's listening interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/rnbguy/dbcop/mockserver/backend"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "mockserver-apalache:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mockserver-apalache", flag.ContinueOnError)
	addr := flags.String("addr", "127.0.0.1:3307", "listen address")
	isoName := flags.String("iso", "read-committed", "isolation: read-committed|repeatable-read|atomic-read|causal|prefix|snapshot-isolation|serializable")
	seed := flags.Uint64("seed", 1, "shared store/run seed; does not seed the external SMT solver")
	rpcURL := flags.String("apalache-rpc", "http://127.0.0.1:8822/rpc", "operator-managed Apalache v0.62.4 explorer endpoint")
	timeout := flags.Duration("apalache-timeout", 30*time.Second, "positive per-operation RPC/solver timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument %q", flags.Arg(0))
	}
	return serve(ctx, *addr, *isoName, *seed, *rpcURL, *timeout)
}

func parseIsolation(name string) (backend.Isolation, error) {
	switch name {
	case "read-committed":
		return backend.ReadCommitted, nil
	case "repeatable-read":
		return backend.RepeatableRead, nil
	case "atomic-read":
		return backend.AtomicRead, nil
	case "causal":
		return backend.Causal, nil
	case "prefix":
		return backend.Prefix, nil
	case "snapshot-isolation":
		return backend.SnapshotIsolation, nil
	case "serializable":
		return backend.Serializable, nil
	default:
		return 0, fmt.Errorf("unknown isolation level %q", name)
	}
}

func serve(ctx context.Context, addr, isoName string, seed uint64, rpcURL string, timeout time.Duration) (err error) {
	iso, err := parseIsolation(isoName)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		return fmt.Errorf("apalache timeout must be positive")
	}
	if ctx.Err() != nil {
		return nil
	}
	backend.RegisterVariables()

	// Construction checks health and a real Init/TakeStep/Result/dispose round trip before listening.
	store, err := backend.NewApalacheStore(seed, seed+1, rpcURL, timeout)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.BeginInitialization(); err != nil {
		return err
	}
	pro := backend.NewProvider(store, iso, nil)
	engine := backend.NewEngine(pro)
	defer func() {
		// BackgroundThreads.Shutdown returns its own expected cancellation.
		if closeErr := engine.Close(); closeErr != context.Canceled {
			err = errors.Join(err, closeErr)
		}
	}()

	cfg := server.Config{Protocol: "tcp", Address: addr}
	s, err := server.NewServer(cfg, engine, sql.NewContext, backend.SessionBuilder(store, iso), nil)
	if err != nil {
		return err
	}
	serving := make(chan error, 1)
	go func() { serving <- s.Start() }()
	defer func() {
		err = errors.Join(err, s.Close())
		if serving != nil {
			err = errors.Join(err, <-serving)
		}
		// Drain SQL handlers before the engine and its explorer/store are closed.
		for _, process := range engine.ProcessList.Processes() {
			engine.ProcessList.Kill(process.Connection)
			err = errors.Join(err, s.SessionManager().KillConnection(process.Connection))
		}
		s.SessionManager().WaitForClosedConnections()
	}()
	fmt.Printf("mockserver-apalache listening on %s iso %s seed %d backend apalache read-policy solver phase initialization\n", addr, isoName, seed)
	fmt.Println("Apalache v0.62.4 requires operator-verified provisioning (health is not version attestation); seed is shared store/run metadata, external SMT solver is not seeded; solver choice is not uniform or fair")
	select {
	case err = <-serving:
		serving = nil
	case <-ctx.Done():
	}
	return err
}
