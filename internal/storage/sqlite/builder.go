package sqlite

import "github.com/Masterminds/squirrel"

// sb is the ONLY placeholder-format decision in the codebase. A
// Postgres implementation copies this file and changes Question → Dollar; no
// query text changes.
//
// It is held on Store as the sb field so it is injectable and so the Postgres
// variant becomes a constructor argument rather than a fork.
//
// Execution convention: always
//
//	sqlStr, args, err := q.ToSql()
//
// followed by an explicit ExecContext/QueryContext on the db or tx handle.
// Never squirrel's RunWith — it obscures which handle is executing, complicates
// the AddSource transaction, and hides the generated SQL from debug logs.
var sb = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Question)
