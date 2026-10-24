package memory

// IndexState returns a deep copy of the persisted-index state, for tests that compare it structurally.
func (r *Repository) IndexState() any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.idx.clone()
}
