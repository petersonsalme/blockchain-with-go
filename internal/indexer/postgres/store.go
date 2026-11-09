// Package postgres is the Postgres adapter of the indexer's Store (ADR-0038), over pgx v5.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
)

// lockKey is the fixed advisory lock key of the single writer.
const lockKey int64 = 0x62776769 // "bwgi"

// ErrLocked is returned when another indexer holds the lock of the database.
var ErrLocked = errors.New("another indexer holds the database lock (pg_advisory_lock 1651992425)")

//go:embed sql/*.sql
var migrations embed.FS

// Store is the Postgres Store. It uses one connection, which also holds the advisory lock for
// its whole life, so it must not be used from two goroutines.
type Store struct {
	conn *pgx.Conn
	hook func(after bool) error // test hook, see export_test.go
}

// Open connects, takes the advisory lock (ErrLocked when it is taken) and applies the schema.
func Open(ctx context.Context, url string) (*Store, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}

	s := &Store{conn: conn}

	var got bool

	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockKey).Scan(&got)
	if err != nil {
		_ = conn.Close(ctx)

		return nil, err
	}

	if !got {
		_ = conn.Close(ctx)

		return nil, ErrLocked
	}

	err = s.migrate(ctx)
	if err != nil {
		_ = conn.Close(ctx)

		return nil, err
	}

	return s, nil
}

// Close releases the lock by closing the connection.
func (s *Store) Close(ctx context.Context) error { return s.conn.Close(ctx) }

// Reset drops the indexer's tables and creates them again, empty.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.conn.Exec(ctx, "DROP TABLE IF EXISTS supply, balances, outputs, blocks, schema_version")
	if err != nil {
		return err
	}

	return s.migrate(ctx)
}

// Tip implements indexer.Store.
func (s *Store) Tip(ctx context.Context) (indexer.Tip, bool, error) {
	var t indexer.Tip

	err := s.conn.QueryRow(ctx, "SELECT height, hash FROM blocks ORDER BY height DESC LIMIT 1").Scan(&t.Height, &t.Hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return indexer.Tip{}, false, nil
	}

	if err != nil {
		return indexer.Tip{}, false, err
	}

	return t, true, nil
}

// Hashes implements indexer.Store.
func (s *Store) Hashes(ctx context.Context, from, limit int) ([]string, error) {
	rows, err := s.conn.Query(ctx, "SELECT hash FROM blocks WHERE height >= $1 ORDER BY height LIMIT $2", from, limit)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func dec(v uint64) string { return strconv.FormatUint(v, 10) }

// Apply implements indexer.Store: the block, its outputs, spends, balances and supply row in one
// transaction. The block must be the child of the stored tip, otherwise ErrNotNext.
func (s *Store) Apply(ctx context.Context, b indexer.Block) (err error) {
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	err = s.checkChild(ctx, tx, b)
	if err != nil {
		return err
	}

	err = s.exec(ctx, tx, "INSERT INTO blocks (height, hash, prev_hash, time, tx_count) VALUES ($1, $2, $3, $4, $5)",
		b.Height, b.Hash, b.PrevHash, b.Time, len(b.Txs))
	if err != nil {
		return err
	}

	var created, spent, coinbase uint64

	deltas := map[string]*big.Int{}
	add := func(addr string, v uint64, negative bool) {
		d, ok := deltas[addr]
		if !ok {
			d = new(big.Int)
			deltas[addr] = d
		}

		x := new(big.Int).SetUint64(v)
		if negative {
			x.Neg(x)
		}

		d.Add(d, x)
	}

	for _, t := range b.Txs {
		for _, in := range t.Inputs {
			var (
				addr   string
				amount string
			)

			err = s.step(func() error {
				return tx.QueryRow(ctx, `UPDATE outputs SET spent_txid = $3, spent_height = $4
					WHERE txid = $1 AND idx = $2 AND spent_height IS NULL RETURNING address, amount::text`,
					in.TxID, int64(in.Index), t.ID, b.Height).Scan(&addr, &amount)
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("block %d: tx %s spends unknown or spent output %s:%d", b.Height, t.ID, in.TxID, in.Index)
			}

			if err != nil {
				return err
			}

			v, perr := strconv.ParseUint(amount, 10, 64)
			if perr != nil {
				return perr
			}

			spent += v
			add(addr, v, true)
		}

		for i, o := range t.Outputs {
			err = s.exec(ctx, tx, "INSERT INTO outputs (txid, idx, height, address, amount) VALUES ($1, $2, $3, $4, $5)",
				t.ID, int64(i), b.Height, o.Address, dec(o.Amount))
			if err != nil {
				return err
			}

			created += o.Amount
			if len(t.Inputs) == 0 {
				coinbase += o.Amount
			}

			add(o.Address, o.Amount, false)
		}
	}

	addrs := make([]string, 0, len(deltas))
	for a := range deltas {
		addrs = append(addrs, a)
	}

	sort.Strings(addrs)

	for _, a := range addrs {
		// A negative net change only ever lowers an existing balance (the CHECK would refuse a
		// negative insert before ON CONFLICT is considered).
		sql := `INSERT INTO balances (address, amount) VALUES ($1, $2)
			ON CONFLICT (address) DO UPDATE SET amount = balances.amount + EXCLUDED.amount`
		if deltas[a].Sign() == -1 {
			sql = "UPDATE balances SET amount = amount + $2::numeric WHERE address = $1"
		}

		err = s.exec(ctx, tx, sql, a, deltas[a].String())
		if err != nil {
			return err
		}
	}

	err = s.exec(ctx, tx, "DELETE FROM balances WHERE amount = 0 AND address = ANY($1)", addrs)
	if err != nil {
		return err
	}

	err = s.exec(ctx, tx, `INSERT INTO supply (height, created, spent, coinbase, total)
		VALUES ($1::bigint, $2, $3, $4, COALESCE((SELECT total FROM supply WHERE height = $1::bigint - 1), 0) + $2::numeric - $3::numeric)`,
		b.Height, dec(created), dec(spent), dec(coinbase))
	if err != nil {
		return err
	}

	return s.step(func() error { return tx.Commit(ctx) })
}

// Rollback implements indexer.Store: heights above to are removed in one transaction, newest
// first in effect: spent outputs come back, created ones go, balances of the touched addresses
// are recomputed from the unspent outputs.
func (s *Store) Rollback(ctx context.Context, to int) error {
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var addrs []string

	err = s.step(func() error {
		rows, qerr := tx.Query(ctx, "SELECT DISTINCT address FROM outputs WHERE height > $1 OR spent_height > $1", to)
		if qerr != nil {
			return qerr
		}

		addrs, qerr = pgx.CollectRows(rows, pgx.RowTo[string])

		return qerr
	})
	if err != nil {
		return err
	}

	slices.Sort(addrs)

	stmts := []struct {
		sql  string
		args []any
	}{
		// Delete first: a txid reused above the fork (ADR-0015) must be gone before the older copy is unspent again.
		{"DELETE FROM outputs WHERE height > $1", []any{to}},
		{"UPDATE outputs SET spent_txid = NULL, spent_height = NULL WHERE spent_height > $1", []any{to}},
		{"DELETE FROM balances WHERE address = ANY($1)", []any{addrs}},
		{`INSERT INTO balances (address, amount) SELECT address, SUM(amount) FROM outputs
			WHERE spent_height IS NULL AND address = ANY($1) GROUP BY address HAVING SUM(amount) > 0`, []any{addrs}},
		{"DELETE FROM supply WHERE height > $1", []any{to}},
		{"DELETE FROM blocks WHERE height > $1", []any{to}},
	}

	for _, st := range stmts {
		err = s.exec(ctx, tx, st.sql, st.args...)
		if err != nil {
			return err
		}
	}

	return s.step(func() error { return tx.Commit(ctx) })
}

// migrate applies, in one transaction, the embedded files above the recorded schema version.
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	_, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)")
	if err != nil {
		return err
	}

	var current int

	err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&current)
	if err != nil {
		return err
	}

	files, err := migrations.ReadDir("sql")
	if err != nil {
		return err
	}

	if current > len(files) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", current, len(files))
	}

	for i := current; i < len(files); i++ {
		body, err := migrations.ReadFile("sql/" + files[i].Name())
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, string(body))
		if err != nil {
			return fmt.Errorf("%s: %w", files[i].Name(), err)
		}

		_, err = tx.Exec(ctx, "INSERT INTO schema_version (version) VALUES ($1)", i+1)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// step runs fn between the two test hook calls.
func (s *Store) step(fn func() error) error {
	if s.hook != nil {
		err := s.hook(false)
		if err != nil {
			return err
		}
	}

	err := fn()
	if err != nil {
		return err
	}

	if s.hook != nil {
		return s.hook(true)
	}

	return nil
}

func (s *Store) exec(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	return s.step(func() error {
		_, err := tx.Exec(ctx, sql, args...)

		return err
	})
}

// checkChild returns ErrNotNext unless b is the genesis of an empty store or tip+1 chained on the tip.
func (s *Store) checkChild(ctx context.Context, tx pgx.Tx, b indexer.Block) error {
	var (
		height int
		hash   string
	)

	err := s.step(func() error {
		return tx.QueryRow(ctx, "SELECT height, hash FROM blocks ORDER BY height DESC LIMIT 1").Scan(&height, &hash)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if b.Height != 0 {
			return indexer.ErrNotNext
		}

		return nil
	}

	if err != nil {
		return err
	}

	if b.Height != height+1 || b.PrevHash != hash {
		return indexer.ErrNotNext
	}

	return nil
}
