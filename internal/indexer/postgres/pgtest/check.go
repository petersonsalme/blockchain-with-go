package pgtest

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Dump returns every table of the indexer, rows in key order, as one string: two databases hold
// the same data exactly when their dumps are equal.
func Dump(tb testing.TB, url string) string {
	tb.Helper()

	ctx := context.Background()
	conn := connect(tb, url)

	defer func() { _ = conn.Close(ctx) }()

	var out strings.Builder

	for _, q := range []struct{ table, order string }{
		{"blocks", "height"},
		{"outputs", "height, txid, idx"},
		{"balances", "address"},
		{"supply", "height"},
	} {
		out.WriteString("-- " + q.table + "\n")

		rows, err := conn.Query(ctx, "SELECT t::text FROM "+q.table+" t ORDER BY "+q.order)
		if err != nil {
			tb.Fatal(err)
		}

		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			tb.Fatal(err)
		}

		for _, l := range lines {
			out.WriteString(l + "\n")
		}
	}

	return out.String()
}

func connect(tb testing.TB, url string) *pgx.Conn {
	tb.Helper()

	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		tb.Fatal(err)
	}

	return conn
}

// CheckInvariants fails t when the committed state breaks an ADR-0038 invariant: blocks form a
// hash chain from the genesis without gaps, balances equal the unspent outputs and none is
// zero, supply at every height is created minus spent up to it.
func CheckInvariants(tb testing.TB, url string) {
	tb.Helper()

	ctx := context.Background()
	conn := connect(tb, url)

	defer func() { _ = conn.Close(ctx) }()

	for _, c := range []struct{ name, query string }{
		{"blocks have a gap or do not start at 0", `SELECT count(*) FROM (SELECT count(*) n, max(height) m, min(height) l FROM blocks) s WHERE n > 0 AND (l <> 0 OR n <> m + 1)`},
		{"prev_hash is not the previous hash", `SELECT count(*) FROM blocks b JOIN blocks p ON p.height = b.height - 1 WHERE b.prev_hash <> p.hash`},
		{"balance differs from unspent outputs", `SELECT count(*) FROM (
			SELECT address, SUM(amount) a FROM outputs WHERE spent_height IS NULL GROUP BY address) u
			FULL JOIN balances b USING (address) WHERE u.a IS DISTINCT FROM b.amount`},
		{"zero or negative balance", `SELECT count(*) FROM balances WHERE amount <= 0`},
		{"supply rows differ from blocks", `SELECT count(*) FROM blocks b FULL JOIN supply s USING (height) WHERE b.hash IS NULL OR s.total IS NULL`},
		{"supply total is not created minus spent", `SELECT count(*) FROM supply s WHERE s.total <>
			(SELECT COALESCE(SUM(amount), 0) FROM outputs WHERE height <= s.height)
			- (SELECT COALESCE(SUM(amount), 0) FROM outputs WHERE spent_height <= s.height)`},
		{"output of a missing block", `SELECT count(*) FROM outputs o WHERE NOT EXISTS (SELECT 1 FROM blocks b WHERE b.height = o.height)
			OR (o.spent_height IS NOT NULL AND NOT EXISTS (SELECT 1 FROM blocks b WHERE b.height = o.spent_height))`},
		{"tip supply differs from sum of balances", `SELECT count(*) FROM supply s WHERE s.height = (SELECT max(height) FROM blocks)
			AND s.total <> (SELECT COALESCE(SUM(amount), 0) FROM balances)`},
	} {
		var n int

		err := conn.QueryRow(ctx, c.query).Scan(&n)
		if err != nil {
			tb.Fatalf("%s: %v", c.name, err)
		}

		if n != 0 {
			tb.Errorf("invariant broken: %s (%d)", c.name, n)
		}
	}
}
