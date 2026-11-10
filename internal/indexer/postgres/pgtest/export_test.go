package pgtest

// DataDir exposes the shared server's data directory to the external tests.
func DataDir() string { return shared.dataDir }
