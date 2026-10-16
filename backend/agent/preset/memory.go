package preset

import "sync"

// MemoryPersister is an in-memory Persister used as the default in NewAgent and
// in tests. It records how many times Save was called so tests can prove the
// "only write when knobs truly changed" rule, and it can be told to fail the
// next Save (to exercise error handling).
type MemoryPersister struct {
	mu        sync.Mutex
	current   Knobs
	existed   bool
	saves     int
	errOnSave error
}

// NewMemoryPersister builds a persister with no prior state.
func NewMemoryPersister() *MemoryPersister { return &MemoryPersister{} }

// NewMemoryPersisterWithState builds a persister pre-seeded with a persisted tuple.
func NewMemoryPersisterWithState(k Knobs) *MemoryPersister {
	return &MemoryPersister{current: k, existed: true}
}

// SetErrorOnSave makes subsequent Save calls return the given error (for testing).
func (p *MemoryPersister) SetErrorOnSave(err error) {
	p.mu.Lock()
	p.errOnSave = err
	p.mu.Unlock()
}

// Saves returns how many times Save was called (a successful write).
func (p *MemoryPersister) Saves() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saves
}

// Load implements Persister.
func (p *MemoryPersister) Load() (Knobs, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current, p.existed, nil
}

// Save implements Persister, recording the write. A configured error is returned
// before any state mutation, so a failed save is never counted.
func (p *MemoryPersister) Save(k Knobs) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.errOnSave != nil {
		return p.errOnSave
	}
	p.current = k
	p.existed = true
	p.saves++
	return nil
}
