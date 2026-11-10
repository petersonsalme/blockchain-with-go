package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()

	pgtest.Stop()
	os.Exit(code)
}

// openStore returns a store over a new database and closes it with the test.
func openStore(t *testing.T) (*postgres.Store, string) {
	t.Helper()

	url := pgtest.NewDatabase(t)

	s, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.Close(context.Background()) })

	return s, url
}
