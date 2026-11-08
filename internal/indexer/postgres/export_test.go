package postgres

// SetHook installs fn, called before and after every statement of Apply and Rollback (after is
// false before the statement); an error it returns aborts the transaction.
func (s *Store) SetHook(fn func(after bool) error) { s.hook = fn }
