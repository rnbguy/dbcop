//go:build cgo

package backend

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/sql"
)

func BenchmarkNativeApalacheSQL(b *testing.B) {
	url := os.Getenv("DBCOP_APALACHE_RPC")
	if url == "" {
		b.Fatal("BenchmarkNativeApalacheSQL requires a real DBCOP_APALACHE_RPC service")
	}
	if os.Getenv("DBCOP_TLC_JAR") == "" {
		b.Fatal("BenchmarkNativeApalacheSQL requires DBCOP_TLC_JAR for untimed history validation")
	}
	RegisterVariables()
	var observations []tlcObservation
	for _, backend := range []string{"native", "apalache"} {
		for _, policy := range []string{"normal", "latest-forced"} {
			b.Run(backend+"/"+policy, func(b *testing.B) {
				b.StopTimer()
				b.Logf("backend=%s isolation=serializable read-policy=%s store-seed-pair=1/2 writer-session=1 reader-session=2; seeds do not seed SMT or equate source distributions", backend, policy)
				if backend == "apalache" {
					b.Log("model=Isolation.tla+OracleStep.tla tool=operator-provisioned-Apalache-v0.62.4 expected-jar-sha256=e5223915507b65cc5f1638bd037ede60f8a4fc1644aff46355779f9b05df9262; health is not version attestation; normal reads retain solver choice")
				} else {
					b.Log("model=native-Rust-checker tool=linked-dbcop_ffi; release artifact/build provenance supplied by benchmark runner; normal reads retain native default selection")
				}
				b.Log("fresh stores per iteration; setup/capability/initialization/validation/trace/cleanup untimed; measured SQL includes all KV/SMT/RPC/disposal; already-running JVM is not cold")
				for iteration := range b.N {
					func() {
						var store *Store
						if backend == "native" {
							store = NewStore(1, 2)
						} else {
							var err error
							store, err = NewApalacheStore(1, 2, url, 30*time.Second)
							if err != nil {
								b.Fatal(err)
							}
						}
						provider := NewProvider(store, Serializable, nil)
						engine := NewEngine(provider)
						writer := NewSession(store, Serializable, 1)
						reader := NewSession(store, Serializable, 2)
						defer func() {
							b.StopTimer()
							writer.SessionEnd()
							reader.SessionEnd()
							if err := engine.Close(); err != nil && !errors.Is(err, context.Canceled) {
								b.Error(err)
							}
							store.Close()
						}()
						writerFixture := &sqlTableFixture{store: store, provider: provider, engine: engine, ctx: sql.NewContext(context.Background(), sql.WithSession(writer))}
						readerFixture := &sqlTableFixture{store: store, provider: provider, engine: engine, ctx: sql.NewContext(context.Background(), sql.WithSession(reader))}
						query := func(f *sqlTableFixture, statement string) []sql.Row {
							_, iter, _, err := f.engine.Query(f.ctx, statement)
							if err != nil {
								b.Fatalf("%s: %v", statement, err)
							}
							rows, err := sql.RowIterToRows(f.ctx, iter)
							if err != nil {
								b.Fatalf("%s: %v", statement, err)
							}
							return rows
						}
						if err := store.BeginInitialization(); err != nil {
							b.Fatal(err)
						}
						for _, statement := range []string{
							"CREATE DATABASE explorer_benchmark", "USE explorer_benchmark",
							"CREATE TABLE items (id INT PRIMARY KEY, v INT)",
							"INSERT INTO items VALUES (1,0)", "CALL dbcop_seal_initial_state()",
						} {
							query(writerFixture, statement)
						}
						query(readerFixture, "USE explorer_benchmark")
						if policy == "latest-forced" {
							query(readerFixture, "SET dbcop_read_latest=1")
						}
						if len(store.CommittedHistory()) != 0 || len(store.Admissions()) != 0 {
							b.Fatal("setup published measured history or source selections")
						}
						db, err := provider.Database(writerFixture.ctx, "explorer_benchmark")
						if err != nil {
							b.Fatal(err)
						}
						table, ok, err := db.GetTableInsensitive(writerFixture.ctx, "items")
						if err != nil || !ok {
							b.Fatalf("initialized items table: found=%t error=%v", ok, err)
						}
						valueKey := cellKey(table.(*Table).storageName, "1", "v")

						b.StartTimer()
						query(writerFixture, "BEGIN")
						query(writerFixture, "UPDATE items SET v=1 WHERE id=1")
						query(writerFixture, "COMMIT")
						rows := query(readerFixture, "SELECT v FROM items")
						b.StopTimer()

						if len(rows) != 1 || len(rows[0]) != 1 {
							b.Fatalf("expected one initialized row and column, got %v", rows)
						}
						value, ok := rows[0][0].(int32)
						if !ok || (value != 0 && value != 1) || (policy == "latest-forced" && value != 1) {
							b.Fatalf("unexpected %s SQL outcome: %v", policy, rows)
						}
						history := store.CommittedHistory()
						if len(history) != 2 || history[0].Session != 1 || history[1].Session != 2 || len(history[1].Writes) != 0 {
							b.Fatalf("expected writer then read-only reader history: %+v", history)
						}
						var written Version
						for _, write := range history[0].Writes {
							if write.Key == valueKey {
								written = write.ID
							}
						}
						if written == 0 {
							b.Fatal("UPDATE did not publish the measured value write")
						}
						readValue := false
						for _, read := range history[1].Reads {
							if read.Key != valueKey {
								continue
							}
							readValue = true
							if (value == 0 && (!read.Initial || read.WriteID != 0)) || (value == 1 && (read.Initial || read.WriteID != written)) {
								b.Fatalf("SQL outcome/source mismatch: selected=%+v writer-version=%d", read, written)
							}
						}
						if !readValue || !checkLevel(buildSessions(history, 0, nil), levelName(Serializable)) {
							b.Fatalf("missing value read or native-invalid emitted history: %+v", history)
						}
						observations = append(observations, tlcObservation{history: history, level: Serializable, enabled: true})
						trace := store.Admissions()
						if len(trace) == 0 {
							b.Fatal("measured SQL did not record selected sources")
						}
						for _, admission := range trace {
							if backend == "apalache" && admission.Admitted != -1 {
								b.Fatal("solver choice was misreported as enumerated admission count")
							}
						}
						b.Logf("iteration=%d selected-source-trace=%+v", iteration+1, trace)
					}()
				}
			})
		}
	}
	verifyTLCObservations(b, observations)
}
