// Command mockserver serves MySQL-wire SQL over a versioned MonkeyDB-style store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/rnbguy/dbcop/mockserver/backend"
)

var (
	readPolicy           = flag.String("read-policy", "uniform", "read selection policy: uniform|sparse|age-stratified|bounded-deviation")
	deviationProbability = flag.Float64("deviation-probability", 0.01, "probability of an older admitted source under sparse or bounded-deviation")
	deviationBudget      = flag.Int("deviation-budget", 1, "maximum older-source deviations per bounded-deviation SQL transaction")
)

func main() {
	addr := flag.String("addr", "127.0.0.1:3307", "listen address")
	isoName := flag.String("iso", "read-committed", "isolation: read-committed|repeatable-read|atomic-read|causal|prefix|snapshot-isolation|serializable")
	seed := flag.Uint64("seed", 1, "RNG seed for read choices")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, *addr, *isoName, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "mockserver:", err)
		os.Exit(1)
	}
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

func serve(ctx context.Context, addr, isoName string, seed uint64) (err error) {
	iso, err := parseIsolation(isoName)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	backend.RegisterVariables()

	store := backend.NewStore(seed, seed+1)
	defer store.Close()
	if err := store.SetReadPolicy(*readPolicy, *deviationProbability); err != nil {
		return err
	}
	if err := store.SetDeviationBudget(*deviationBudget); err != nil {
		return err
	}
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

	cfg := server.Config{
		Protocol: "tcp",
		Address:  addr,
	}
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
		// Drain handlers before releasing the store's native oracle handles.
		for _, process := range engine.ProcessList.Processes() {
			engine.ProcessList.Kill(process.Connection)
			err = errors.Join(err, s.SessionManager().KillConnection(process.Connection))
		}
		s.SessionManager().WaitForClosedConnections()
	}()
	fmt.Printf("mockserver listening on %s iso %s seed %d read-policy %s deviation-probability %g deviation-budget %d phase initialization\n", addr, isoName, seed, *readPolicy, *deviationProbability, *deviationBudget)
	select {
	case err = <-serving:
		serving = nil
	case <-ctx.Done():
	}
	return err
}
