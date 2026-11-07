package sqlite

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Masterminds/squirrel"

	"github.com/borisovdev/newsbuddy/internal/model"
)

// filterChunk bounds how many keys go into one IN (...) list. SQLite's default
// variable limit is 32766 (999 on older builds); 500 keeps a wide safety margin
// on every build.
const filterChunk = 500

// pendingWhere is the one definition of "pending": inserted by the post loop,
// never published, never already swept. Seed watermark rows (seeded=1) are
// excluded by construction, which is what keeps the no-backfill watermark out of the
// startup abandon sweep.
var pendingWhere = squirrel.Eq{
	"posted_at":    nil,
	"seeded":       0,
	"abandoned_at": nil,
}

// FilterUnseen returns the subset of keys with NO row for sourceID, preserving
// input order. It chunks internally so a large feed cannot blow the variable
// limit.
func (s *Store) FilterUnseen(ctx context.Context, sourceID int64, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	// Known keys are collected as a sorted []string and probed with
	// BinarySearch, because the cache-boundary check bans keyed-map lookups outside internal/cache.
	var known []string
	for start := 0; start < len(keys); start += filterChunk {
		end := min(start+filterChunk, len(keys))

		q := s.sb.Select("item_key").From("seen_items").
			Where(squirrel.Eq{"source_id": sourceID, "item_key": keys[start:end]})
		sqlStr, args, err := q.ToSql()
		if err != nil {
			return nil, fmt.Errorf("sqlite: build FilterUnseen query: %w", err)
		}
		rows, err := s.db.QueryContext(ctx, sqlStr, args...)
		if err != nil {
			return nil, mapErr(err)
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return nil, mapErr(err)
			}
			known = append(known, k)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		rows.Close()
	}
	slices.Sort(known)

	out := make([]string, 0, len(keys)-len(known))
	for _, k := range keys {
		if _, found := slices.BinarySearch(known, k); !found {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// InsertPending inserts (sourceID, key) with posted_at=NULL, seeded=0 and
// reports whether the row was new. This is THE dedup gate: the
// PRIMARY KEY (source_id, item_key) decides, not application logic.
//
// A FOREIGN KEY violation means the source was deleted mid-cycle, so
// it surfaces as ErrSourceGone.
func (s *Store) InsertPending(ctx context.Context, sourceID int64, key string) (bool, error) {
	q := s.sb.Insert("seen_items").
		Columns("source_id", "item_key", "first_seen_at", "posted_at", "seeded").
		Values(sourceID, key, fmtTime(time.Now()), nil, 0).
		Suffix("ON CONFLICT (source_id, item_key) DO NOTHING")
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return false, fmt.Errorf("sqlite: build InsertPending statement: %w", err)
	}
	res, err := s.db.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: InsertPending rows affected: %w", err)
	}
	return n == 1, nil
}

// MarkPosted records the published message against an existing pending row.
func (s *Store) MarkPosted(ctx context.Context, sourceID int64, key string, messageID int64, at time.Time) error {
	q := s.sb.Update("seen_items").
		Set("posted_at", fmtTime(at)).
		Set("message_id", messageID).
		Where(squirrel.Eq{"source_id": sourceID, "item_key": key})
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("sqlite: build MarkPosted statement: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, sqlStr, args...); err != nil {
		return mapErr(err)
	}
	return nil
}

// AbandonPending stamps abandoned_at on every pending row and returns what it
// stamped, so the caller can log each exactly once. The rows are
// selected and updated inside one transaction, so the returned list is exactly
// the set that was stamped. Startup only.
func (s *Store) AbandonPending(ctx context.Context, at time.Time) ([]model.AbandonedItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin AbandonPending: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the tx is committed

	sel := s.sb.Select("source_id", "item_key").From("seen_items").
		Where(pendingWhere).
		OrderBy("source_id ASC", "item_key ASC")
	sqlStr, args, err := sel.ToSql()
	if err != nil {
		return nil, fmt.Errorf("sqlite: build AbandonPending query: %w", err)
	}
	rows, err := tx.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	var out []model.AbandonedItem
	for rows.Next() {
		var it model.AbandonedItem
		if err := rows.Scan(&it.SourceID, &it.ItemKey); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, mapErr(err)
	}
	rows.Close()

	if len(out) == 0 {
		return nil, tx.Commit()
	}

	upd := s.sb.Update("seen_items").Set("abandoned_at", fmtTime(at)).Where(pendingWhere)
	sqlStr, args, err = upd.ToSql()
	if err != nil {
		return nil, fmt.Errorf("sqlite: build AbandonPending statement: %w", err)
	}
	if _, err := tx.ExecContext(ctx, sqlStr, args...); err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// CountPostedSince counts posted rows at or after since. Timestamps are
// RFC3339 Z, so the lexicographic >= is a chronological >=.
func (s *Store) CountPostedSince(ctx context.Context, since time.Time) (int, error) {
	q := s.sb.Select("COUNT(*)").From("seen_items").
		Where(squirrel.GtOrEq{"posted_at": fmtTime(since)})
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return 0, fmt.Errorf("sqlite: build CountPostedSince query: %w", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, sqlStr, args...).Scan(&n); err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
