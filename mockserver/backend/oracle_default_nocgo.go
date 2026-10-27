//go:build !cgo

package backend

// NewStore requires the native oracle; pure-Go callers must select the explorer backend.
func NewStore(seed1, seed2 uint64) *Store {
	panic("native oracle requires cgo; use NewApalacheStore")
}
