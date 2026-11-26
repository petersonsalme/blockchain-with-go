package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
)

var _ indexer.Reader = (*Reader)(nil)

// Reader implements indexer.Reader against Postgres using REPEATABLE READ read-only transactions.
type Reader struct {
	url string
}

// NewReader returns a Reader that opens a read-only connection per call without taking the writer lock.
func NewReader(url string) *Reader {
	return &Reader{url: url}
}

// Health returns the indexed tip or ErrHeightNotIndexed when no block is indexed yet.
func (r *Reader) Health(ctx context.Context) (indexer.Tip, error) {
	var tip indexer.Tip

	err := r.withReadTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT height, hash FROM blocks ORDER BY height DESC LIMIT 1").Scan(&tip.Height, &tip.Hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return indexer.ErrHeightNotIndexed
		}

		return err
	})

	return tip, err
}

// SupplyTip returns the supply snapshot at the indexed tip.
func (r *Reader) SupplyTip(ctx context.Context) (indexer.SupplyRow, error) {
	var row indexer.SupplyRow

	err := r.withReadTx(ctx, func(tx pgx.Tx) error {
		var qerr error

		row, qerr = scanSupplyRow(tx.QueryRow(ctx, `
			SELECT b.height, b.hash, s.total::text, s.created::text, s.spent::text, s.coinbase::text
			FROM blocks b JOIN supply s ON s.height = b.height
			ORDER BY b.height DESC LIMIT 1`))

		return qerr
	})

	return row, err
}

// SupplyAt returns the supply snapshot at height, or ErrHeightNotIndexed if not indexed.
func (r *Reader) SupplyAt(ctx context.Context, height int) (indexer.SupplyRow, error) {
	var row indexer.SupplyRow

	err := r.withReadTx(ctx, func(tx pgx.Tx) error {
		var qerr error

		row, qerr = scanSupplyRow(tx.QueryRow(ctx, `
			SELECT b.height, b.hash, s.total::text, s.created::text, s.spent::text, s.coinbase::text
			FROM blocks b JOIN supply s ON s.height = b.height
			WHERE b.height = $1`, height))

		return qerr
	})

	return row, err
}

func scanSupplyRow(r pgx.Row) (indexer.SupplyRow, error) {
	var row indexer.SupplyRow

	err := r.Scan(&row.Height, &row.Hash, &row.Total, &row.Created, &row.Spent, &row.Coinbase)
	if errors.Is(err, pgx.ErrNoRows) {
		return indexer.SupplyRow{}, indexer.ErrHeightNotIndexed
	}

	return row, err
}

// Richest returns the top limit addresses by balance at the indexed tip.
func (r *Reader) Richest(ctx context.Context, limit int) (indexer.RichestSnapshot, error) {
	snap := indexer.RichestSnapshot{Addresses: []indexer.RichAddress{}}

	err := r.withReadTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT b.height, b.hash, s.total::text
			FROM blocks b JOIN supply s ON s.height = b.height
			ORDER BY b.height DESC LIMIT 1`).Scan(&snap.Height, &snap.Hash, &snap.Total)
		if errors.Is(err, pgx.ErrNoRows) {
			return indexer.ErrHeightNotIndexed
		}

		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT address, amount::text
			FROM balances
			WHERE amount > 0
			ORDER BY balances.amount DESC, balances.address ASC
			LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var addr, balance string

			err = rows.Scan(&addr, &balance)
			if err != nil {
				return err
			}

			snap.Addresses = append(snap.Addresses, indexer.RichAddress{
				Address: addr,
				Balance: balance,
				Share:   indexer.ShareString(balance, snap.Total),
			})
		}

		return rows.Err()
	})

	return snap, err
}

// Stats returns aggregated block, transaction and supply totals over heights from..to inclusive.
func (r *Reader) Stats(ctx context.Context, from, to int) (indexer.RangeStats, error) {
	var stats indexer.RangeStats

	err := r.withReadTx(ctx, func(tx pgx.Tx) error {
		var tipHeight int

		err := tx.QueryRow(ctx, "SELECT height FROM blocks ORDER BY height DESC LIMIT 1").Scan(&tipHeight)
		if errors.Is(err, pgx.ErrNoRows) {
			return indexer.ErrHeightNotIndexed
		}

		if err != nil {
			return err
		}

		if to > tipHeight {
			to = tipHeight
		}

		if from > to {
			stats = indexer.RangeStats{
				From:     from,
				To:       to,
				Blocks:   0,
				Txs:      0,
				Created:  "0",
				Spent:    "0",
				Coinbase: "0",
			}

			return nil
		}

		var (
			blocks                   int
			txs                      int
			created, spent, coinbase string
		)

		err = tx.QueryRow(ctx, `
			SELECT COUNT(*), COALESCE(SUM(b.tx_count), 0),
			       COALESCE(SUM(s.created), 0)::text, COALESCE(SUM(s.spent), 0)::text, COALESCE(SUM(s.coinbase), 0)::text
			FROM blocks b JOIN supply s ON s.height = b.height
			WHERE b.height BETWEEN $1 AND $2`, from, to).Scan(&blocks, &txs, &created, &spent, &coinbase)
		if err != nil {
			return err
		}

		stats = indexer.RangeStats{
			From:     from,
			To:       to,
			Blocks:   blocks,
			Txs:      txs,
			Created:  created,
			Spent:    spent,
			Coinbase: coinbase,
		}

		return nil
	})

	return stats, err
}

func (r *Reader) withReadTx(ctx context.Context, fn func(pgx.Tx) error) error {
	conn, err := pgx.Connect(ctx, r.url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = fn(tx)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
