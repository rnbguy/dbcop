// Package backend: seeded RNG helper.
package backend

import (
	"math/rand/v2"
	"sync"
)

type lockedRand struct {
	mu sync.Mutex
	r  *rand.Rand
}

func newLockedRand(seed1, seed2 uint64) *lockedRand {
	return &lockedRand{r: rand.New(rand.NewPCG(seed1, seed2))}
}

// Intn returns a uniform value in [0, n).
func (l *lockedRand) Intn(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.IntN(n)
}

// Float64 returns a uniform value in [0, 1).
func (l *lockedRand) Float64() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Float64()
}
