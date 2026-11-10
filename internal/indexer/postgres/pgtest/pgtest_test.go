package pgtest_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()

	pgtest.Stop()
	os.Exit(code)
}

func exec(t *testing.T, url string, sql ...string) {
	t.Helper()

	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	for _, s := range sql {
		_, err = conn.Exec(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
	}
}

const schema = `CREATE TABLE blocks(height int, hash text, prev_hash text);
CREATE TABLE outputs(txid text, idx int, height int, address text, amount numeric, spent_txid text, spent_height int);
CREATE TABLE balances(address text, amount numeric);
CREATE TABLE supply(height int, total numeric)`

func TestNewDatabaseGivesDistinctEmptyDatabases(t *testing.T) {
	a, b := pgtest.NewDatabase(t), pgtest.NewDatabase(t)
	if a == b || !strings.HasPrefix(a, "postgres://") {
		t.Fatalf("urls %q %q", a, b)
	}

	exec(t, a, "CREATE TABLE only_in_a(x int)")

	conn, err := pgx.Connect(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var n int

	err = conn.QueryRow(context.Background(), "SELECT count(*) FROM pg_tables WHERE tablename = 'only_in_a'").Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("database b sees a's table: %d %v", n, err)
	}
}

func TestDumpListsRowsInKeyOrder(t *testing.T) {
	url := pgtest.NewDatabase(t)
	exec(t, url, schema,
		"INSERT INTO blocks VALUES (1,'h1','h0'),(0,'h0','')",
		"INSERT INTO balances VALUES ('b',2),('a',1)")

	got := pgtest.Dump(t, url)
	want := "-- blocks\n(0,h0,\"\")\n(1,h1,h0)\n-- outputs\n-- balances\n(a,1)\n(b,2)\n-- supply\n"

	if got != want {
		t.Fatalf("dump\n%s\nwant\n%s", got, want)
	}
}

// recorder is a testing.TB that records Errorf instead of failing the test.
type recorder struct {
	testing.TB

	errs []string
}

func (r *recorder) Errorf(format string, args ...any) { r.errs = append(r.errs, format) }
func (r *recorder) Fatalf(format string, args ...any) { r.errs = append(r.errs, format) }

func TestCheckInvariantsAcceptsAConsistentChainAndNamesEachBreak(t *testing.T) {
	good := pgtest.NewDatabase(t)
	exec(t, good, schema,
		"INSERT INTO blocks VALUES (0,'h0',''),(1,'h1','h0')",
		"INSERT INTO outputs VALUES ('t0',0,0,'a',10,NULL,NULL),('t1',0,1,'b',4,NULL,NULL),('t1',1,1,'a',6,NULL,NULL)",
		"INSERT INTO outputs VALUES ('t0',1,0,'a',5,'t1',1)",
		"INSERT INTO balances VALUES ('a',16),('b',4)",
		"INSERT INTO supply VALUES (0,15),(1,20)")

	r := &recorder{TB: t}
	pgtest.CheckInvariants(r, good)

	if len(r.errs) != 0 {
		t.Fatalf("consistent state flagged: %v", r.errs)
	}

	for name, breakIt := range map[string]string{
		"gap":          "DELETE FROM blocks WHERE height = 0",
		"prev":         "UPDATE blocks SET prev_hash = 'zz' WHERE height = 1",
		"balance":      "UPDATE balances SET amount = 17 WHERE address = 'a'",
		"zero balance": "INSERT INTO balances VALUES ('z', 0)",
		"supply row":   "DELETE FROM supply WHERE height = 1",
		"supply total": "UPDATE supply SET total = 99 WHERE height = 0",
		"orphan":       "INSERT INTO outputs VALUES ('t9',0,7,'q',1,NULL,NULL)",
	} {
		url := pgtest.NewDatabase(t)
		exec(t, url, schema,
			"INSERT INTO blocks VALUES (0,'h0',''),(1,'h1','h0')",
			"INSERT INTO outputs VALUES ('t0',0,0,'a',10,NULL,NULL),('t1',0,1,'b',4,NULL,NULL),('t1',1,1,'a',6,NULL,NULL)",
			"INSERT INTO outputs VALUES ('t0',1,0,'a',5,'t1',1)",
			"INSERT INTO balances VALUES ('a',16),('b',4)",
			"INSERT INTO supply VALUES (0,15),(1,20)",
			breakIt)

		r := &recorder{TB: t}
		pgtest.CheckInvariants(r, url)

		if len(r.errs) == 0 {
			t.Errorf("%s: broken state not flagged", name)
		}
	}
}
