package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Masterminds/squirrel"

	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// seedChunk is how many seed rows go into one multi-row INSERT.
const seedChunk = 200

// sourceColumns is the fixed select list, matching scanSource's argument order.
var sourceColumns = []string{
	"id", "feed_url", "title", "created_at", "last_polled_at",
	"last_success_at", "last_error", "consecutive_failures", "etag", "last_modified",
}

// AddSource inserts the source and seeds seenKeys in ONE transaction. Seed rows are written with seeded=1 and posted_at=NULL, so they act
// as a watermark: never posted, never abandoned.
func (s *Store) AddSource(ctx context.Context, src model.Source, seedKeys []string) (model.Source, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Source{}, fmt.Errorf("sqlite: begin AddSource: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the tx is committed

	insert := s.sb.Insert("sources").
		Columns("feed_url", "title", "created_at", "last_polled_at", "last_success_at", "etag", "last_modified").
		Values(src.FeedURL, src.Title, fmtTime(src.CreatedAt),
			fmtTimePtr(src.LastPolledAt), fmtTimePtr(src.LastSuccessAt),
			src.ETag, src.LastModified)
	sqlStr, args, err := insert.ToSql()
	if err != nil {
		return model.Source{}, fmt.Errorf("sqlite: build source insert: %w", err)
	}
	res, err := tx.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return model.Source{}, mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Source{}, fmt.Errorf("sqlite: source id: %w", err)
	}

	firstSeen := fmtTime(src.CreatedAt)
	for start := 0; start < len(seedKeys); start += seedChunk {
		end := min(start+seedChunk, len(seedKeys))

		q := s.sb.Insert("seen_items").
			Columns("source_id", "item_key", "first_seen_at", "posted_at", "seeded").
			Suffix("ON CONFLICT (source_id, item_key) DO NOTHING")
		for _, key := range seedKeys[start:end] {
			q = q.Values(id, key, firstSeen, nil, 1)
		}
		sqlStr, args, err := q.ToSql()
		if err != nil {
			return model.Source{}, fmt.Errorf("sqlite: build seed insert: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlStr, args...); err != nil {
			return model.Source{}, mapErr(err)
		}
	}

	if err := tx.Commit(); err != nil {
		return model.Source{}, mapErr(err)
	}

	out := src
	out.ID = id
	return out, nil
}

func (s *Store) GetSourceByID(ctx context.Context, id int64) (model.Source, error) {
	return s.getSource(ctx, squirrel.Eq{"id": id})
}

func (s *Store) GetSourceByURL(ctx context.Context, normalizedURL string) (model.Source, error) {
	return s.getSource(ctx, squirrel.Eq{"feed_url": normalizedURL})
}

func (s *Store) getSource(ctx context.Context, where squirrel.Sqlizer) (model.Source, error) {
	q := s.sb.Select(sourceColumns...).From("sources").Where(where)
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return model.Source{}, fmt.Errorf("sqlite: build source query: %w", err)
	}
	src, err := scanSource(s.db.QueryRowContext(ctx, sqlStr, args...))
	if err != nil {
		return model.Source{}, mapErr(err)
	}
	return src, nil
}

// ListSources returns every source ordered by id ASC.
func (s *Store) ListSources(ctx context.Context) ([]model.Source, error) {
	q := s.sb.Select(sourceColumns...).From("sources").OrderBy("id ASC")
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("sqlite: build ListSources query: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []model.Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// DeleteSource removes the source; its seen_items rows go with it through
// ON DELETE CASCADE, which is why the foreign_keys pragma assertion in
// Open is not optional.
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	q := s.sb.Delete("sources").Where(squirrel.Eq{"id": id})
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("sqlite: build DeleteSource statement: %w", err)
	}
	res, err := s.db.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: DeleteSource rows affected: %w", err)
	}
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// UpdateSourcePollResult writes last_polled_at plus, on success,
// last_success_at, consecutive_failures=0, a cleared last_error, and the
// validators; on failure, last_error and consecutive_failures+1.
//
// StoreValidators is honored ONLY when Success is true. A
// failure result never touches etag/last_modified, whatever its validator
// fields hold — a failed poll says nothing about whether the cached copy is
// still current, so wiping the validators there would force a full body fetch
// after every transient error.
func (s *Store) UpdateSourcePollResult(ctx context.Context, r model.PollResult) error {
	q := s.sb.Update("sources").
		Set("last_polled_at", fmtTime(r.PolledAt)).
		Where(squirrel.Eq{"id": r.SourceID})

	if r.Success {
		q = q.
			Set("last_success_at", fmtTime(r.PolledAt)).
			Set("consecutive_failures", 0).
			Set("last_error", "")
		if r.StoreValidators {
			q = q.Set("etag", r.ETag).Set("last_modified", r.LastModified)
		} else {
			// A truncated cycle drops the validators so the next poll
			// refetches the full body.
			q = q.Set("etag", "").Set("last_modified", "")
		}
	} else {
		q = q.
			Set("last_error", r.ErrMessage).
			Set("consecutive_failures", squirrel.Expr("consecutive_failures + 1"))
	}

	sqlStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("sqlite: build UpdateSourcePollResult statement: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, sqlStr, args...); err != nil {
		return mapErr(err)
	}
	return nil
}

// rowScanner covers both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(sc rowScanner) (model.Source, error) {
	var (
		src           model.Source
		createdAt     string
		lastPolledAt  sql.NullString
		lastSuccessAt sql.NullString
	)
	err := sc.Scan(
		&src.ID, &src.FeedURL, &src.Title, &createdAt, &lastPolledAt,
		&lastSuccessAt, &src.LastError, &src.ConsecutiveFailures, &src.ETag, &src.LastModified,
	)
	if err != nil {
		return model.Source{}, err
	}
	if src.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Source{}, err
	}
	if src.LastPolledAt, err = nullTime(lastPolledAt); err != nil {
		return model.Source{}, err
	}
	if src.LastSuccessAt, err = nullTime(lastSuccessAt); err != nil {
		return model.Source{}, err
	}
	return src, nil
}
