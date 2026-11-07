PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS sources (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    feed_url             TEXT    NOT NULL,
    title                TEXT    NOT NULL,
    created_at           TEXT    NOT NULL,
    last_polled_at       TEXT,
    last_success_at      TEXT,
    last_error           TEXT    NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    etag                 TEXT    NOT NULL DEFAULT '',
    last_modified        TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_sources_feed_url ON sources (feed_url);

CREATE TABLE IF NOT EXISTS seen_items (
    source_id     INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    item_key      TEXT    NOT NULL,
    first_seen_at TEXT    NOT NULL,
    posted_at     TEXT,
    abandoned_at  TEXT,
    seeded        INTEGER NOT NULL DEFAULT 0,
    message_id    INTEGER,
    PRIMARY KEY (source_id, item_key)
);

CREATE INDEX IF NOT EXISTS ix_seen_items_posted_at
    ON seen_items (posted_at) WHERE posted_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_seen_items_pending
    ON seen_items (source_id)
    WHERE posted_at IS NULL AND seeded = 0 AND abandoned_at IS NULL;
