package chain

import "github.com/petersonsalme/blockchain-with-go/internal/storage"

// SetFitLimit lowers the pool selection budget of s so that selection tests
// need not build megabyte-sized pools. It exists only in test builds (ADR-0032).
func (s *Service) SetFitLimit(limit int) { s.fitLimit = limit }

// SwapRepo replaces the repository of s, so verify tests can serve tampered blocks.
func (s *Service) SwapRepo(r storage.IndexedRepository) { s.repo = r }
