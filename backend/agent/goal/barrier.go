package goal

import "sync"

// Barrier is a reusable (cyclic) barrier: a synchronization fence where parties
// goroutines block until all have arrived, at which point every waiter is released
// and the barrier resets for the next generation. It is the "竞态栅栏" primitive
// the agent loop can use to fence round boundaries when running with parallelism —
// no goroutine may advance to the next round until every in-flight step of the
// current round has settled.
//
// A barrier configured with parties == 1 is a no-op: the single caller passes
// immediately. parties must be >= 1.
type Barrier struct {
	mu        sync.Mutex
	cond      *sync.Cond
	parties   int
	waiting   int
	generation int
}

// NewBarrier creates a barrier that releases when parties goroutines have called
// Wait. parties must be >= 1.
func NewBarrier(parties int) *Barrier {
	if parties < 1 {
		parties = 1
	}
	b := &Barrier{parties: parties}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Parties returns the configured party count.
func (b *Barrier) Parties() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.parties
}

// SetParties reconfigures the party count (>= 1). It takes effect on the next
// generation; an in-flight generation is not disturbed.
func (b *Barrier) SetParties(n int) {
	if n < 1 {
		n = 1
	}
	b.mu.Lock()
	b.parties = n
	b.mu.Unlock()
}

// Wait blocks until every party has arrived. When the last party arrives, the
// barrier resets (waiting -> 0) and bumps the generation, releasing all waiters.
// Each call observes a single generation, so a goroutine that arrives after the
// reset waits for the next full cohort.
func (b *Barrier) Wait() {
	b.mu.Lock()
	gen := b.generation
	b.waiting++
	if b.waiting == b.parties {
		b.waiting = 0
		b.generation++
		b.cond.Broadcast()
		b.mu.Unlock()
		return
	}
	for b.generation == gen {
		b.cond.Wait()
	}
	b.mu.Unlock()
}
