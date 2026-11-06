package chain

// SetFitLimit lowers the pool selection budget of s so that selection tests
// need not build megabyte-sized pools. It exists only in test builds (ADR-0032).
func (s *Service) SetFitLimit(limit int) { s.fitLimit = limit }
