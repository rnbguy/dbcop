//go:build cgo

package backend

/*
#cgo !dbcop_release LDFLAGS: -L${SRCDIR}/../../target/debug
#cgo dbcop_release LDFLAGS: -L${SRCDIR}/../../target/release
#cgo LDFLAGS: -ldbcop_ffi -ldl -lm
#include <stdlib.h>
#include <stdint.h>

// Returns 1 if history satisfies level, 0 if violated, -1 on bad input.
int dbcop_check(const char *history_json, const char *level);
void *dbcop_oracle_new(const char *level);
void dbcop_oracle_free(void *handle);
int dbcop_oracle_check(void *handle, uint64_t session_id, const char *events_json, int commit);
int dbcop_oracle_read(void *handle, uint64_t session_id, uint64_t variable, uint64_t version, int initial, int accept);
void dbcop_oracle_reset(void *handle);
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"unsafe"
)

// NewStore returns an empty store with seeded, uniform admitted reads.
func NewStore(seed1, seed2 uint64) *Store {
	return newStore(seed1, seed2, newOracleSet(newNativeDriver, nil))
}

type cgoNativeDriver struct {
	handle unsafe.Pointer
}

func newNativeDriver(iso Isolation) nativeDriver {
	level := C.CString(levelName(iso))
	handle := C.dbcop_oracle_new(level)
	C.free(unsafe.Pointer(level))
	if handle == nil {
		panic("cannot create persistent consistency oracle")
	}
	return &cgoNativeDriver{handle: handle}
}

// checkLevel asks dbcop_core whether sessions satisfy level.
func checkLevel(sessions [][]historyTxn, level string) bool {
	buf, err := json.Marshal(sessions)
	if err != nil {
		return false
	}
	ch := C.CString(string(buf))
	defer C.free(unsafe.Pointer(ch))
	cl := C.CString(level)
	defer C.free(unsafe.Pointer(cl))
	return C.dbcop_check(ch, cl) == 1
}

func (driver *cgoNativeDriver) read(session int, variable uint64, version Version, initial, accept bool) bool {
	root, retain := C.int(0), C.int(0)
	if initial {
		root = 1
		version = 0
	}
	if accept {
		retain = 1
	}
	result := C.dbcop_oracle_read(driver.handle, C.uint64_t(session), C.uint64_t(variable), C.uint64_t(version), root, retain)
	if result < 0 {
		panic("persistent oracle rejected an internal read")
	}
	return result == 1
}

func (driver *cgoNativeDriver) check(session int, events []historyEvent, commit bool) bool {
	buf, err := json.Marshal(events)
	if err != nil {
		panic(fmt.Errorf("encode oracle transaction: %w", err))
	}
	input := C.CString(string(buf))
	defer C.free(unsafe.Pointer(input))
	publish := C.int(0)
	if commit {
		publish = 1
	}
	result := C.dbcop_oracle_check(driver.handle, C.uint64_t(session), input, publish)
	if result < 0 {
		panic("persistent oracle rejected an internal transaction encoding")
	}
	return result == 1
}

func (driver *cgoNativeDriver) reset() { C.dbcop_oracle_reset(driver.handle) }

func (driver *cgoNativeDriver) close() {
	C.dbcop_oracle_free(driver.handle)
	driver.handle = nil
}
