# news-buddy

A personal single-user Telegram bot that crawls RSS/Atom feeds and posts new articles to a Telegram channel. Avoids duplicate posts, requires no backfill of feed history on first add, and stores all state in SQLite.

## What it does

- Polls a configurable set of RSS/Atom feeds at regular intervals (default 5 minutes).
- Posts each newly published item to a single Telegram channel.
- Tracks which items have been seen (deduplicated per source).
- **No backfill**: when you add a feed, its existing items are marked as already seen without posting them.
- Accessible only to one owner via Telegram bot commands.

## Bot commands

All commands are owner-only and accepted in the bot's private chat only.

| Command | Syntax | Description |
|---|---|---|
| `/start` | `/start` | Greeting and command list. |
| `/help` | `/help` | Lists all available commands with syntax. |
| `/add` | `/add <feed_url> [display name]` | Adds a new feed source. Validates, fetches, and parses before persisting. Replies with the assigned id and resolved title. |
| `/list` | `/list` | Shows all sources: id, title, URL, last success timestamp, and (if failing) the last error. |
| `/remove` | `/remove <id_or_url>` | Removes a source by its id (from `/list`) or by its full feed URL. |
| `/status` | `/status` | Displays uptime, source count, items posted in the last 24 hours, and the last poll cycle duration. |

Unknown commands produce a usage hint. Non-owner messages are silently ignored.

## Configuration

All configuration via environment variables. No config file required.

| Variable | Required | Default | Notes |
|---|---|---|---|
| `TELEGRAM_BOT_TOKEN` | yes | — | Telegram bot token (format: `numeric_id:base64_string`) |
| `TELEGRAM_CHANNEL_ID` | yes | — | Target channel id (`@channelname` or `-100...` for private channels) |
| `TELEGRAM_OWNER_ID` | yes | — | Numeric Telegram user id of the sole owner |
| `DB_PATH` | no | `/data/news-buddy.db` (in the image) | Path to the SQLite database file inside the container |
| `POLL_INTERVAL` | no | `5m` | How often to poll feeds (e.g., `5m`, `1h`) |
| `HTTP_TIMEOUT` | no | `15s` | Timeout for fetching feed URLs |
| `EXCERPT_LEN` | no | `300` | Maximum excerpt length (1–2000 characters) |
| `MAX_ITEMS_PER_POLL` | no | `20` | Max items to post per source per cycle (1–100) |
| `LOG_LEVEL` | no | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | no | `json` | Log format: `json` or `text` |

## Quick start

The service runs only in Docker.

```bash
cp deploy/.env.example deploy/.env
# Edit deploy/.env and fill in the three required environment variables
make up      # build the image and start the container in the background
make logs    # follow logs
make down    # stop
```

The Docker Compose setup automatically creates and migrates the SQLite database in a named volume.

## Notes

### Docker deployment: directory permissions

When using Docker, the database file is written by the `distroless` `nonroot` user (uid 65532). The image ships `/data` owned by this uid, so a fresh named volume (as in the Compose file) is writable out of the box. If you bind-mount a host directory instead, `chown 65532:65532` it first.

### Single-instance requirement (important)

Running two instances of this service simultaneously against the same database will cause duplicate posts and collide on Telegram's `getUpdates` long-polling endpoint with a 409 error. **Only one instance per database file.** This is documented here as a hard constraint but not enforced by the software itself—it relies on operator discipline and SQLite's file lock.

### At-most-once posting

The service prioritizes "never duplicate" over "never miss." The ordering is: write the item row to the database → send the message to Telegram → mark it as posted. If the process crashes between the send and the mark, that item is abandoned on restart and logged as `post.abandoned`. It will not be re-sent. This is a deliberate tradeoff.

### No-backfill behavior

When you `/add` a feed, every item currently present in that feed is recorded as already-seen without being posted. Only items published **after** the add time will appear in the channel. Consequently:

- Removing and re-adding the same feed URL will **not** re-post its history.
- This prevents an old feed from flooding the channel with its entire archive on first add.
- The watermark is the seen-key set (per item), not a publish-date cutoff, since many real feeds have missing or wrong dates.

### Per-source deduplication

Duplicate detection is scoped **per source**. If you add two feeds that both syndicate the same article, it will be posted twice (once per source). This is correct behavior for a personal relay; global dedup can be added later if needed.

## Manual verification checklist

Before deploying to production, perform these manual checks:

1. **Boot the service** with `make up` after filling `deploy/.env`.
   - Confirm the startup log shows the bot's username (a `startup` event with `bot_username`).

2. **Add a real feed** with `/add <url>`.
   - Confirm zero messages appear in the channel.
   - Confirm the reply shows the correct count of seeded items.

3. **Wait for one poll cycle** after the feed publishes a new item.
   - Confirm exactly one new message appears.
   - Confirm the message is correctly formatted with a working link and no link preview.

4. **Test `/list`, `/status`, and `/remove`** commands.
   - Ensure `/list` shows all sources with correct metadata.
   - Ensure `/remove` deletes sources and their history.

5. **Graceful shutdown**: run `make down` (sends SIGTERM).
   - Confirm it exits with code 0.
   - Confirm a `shutdown` log line is emitted.

6. **Verify token redaction**: grep `docker compose -f deploy/docker-compose.yml logs` for your bot token.
   - Should return zero matches.

Everything else is covered by the automated, offline test suite (`make test`).

## License

Copyright (C) 2026 Aleksandr Borisov

This program is free software: you can redistribute it and/or modify it under the terms of the [GNU Affero General Public License v3.0](LICENSE) as published by the Free Software Foundation. It is distributed WITHOUT ANY WARRANTY; see the license for details.

If you run a modified version as a network service, the AGPL requires you to make your modified source code available to its users.
