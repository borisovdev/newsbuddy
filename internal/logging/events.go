package logging

// Event names used as the slog message string. Declared as constants so
// grep and `jq 'select(.msg=="post.sent")'` both work.
const (
	EvStartup  = "startup"  // level info  | attrs: config (redacted), version
	EvShutdown = "shutdown" // info        | reason, duration_ms, forced

	EvSourceAdded   = "source.added"   // info        | source_id, feed_url, title
	EvSourceRemoved = "source.removed" // info        | source_id, feed_url
	EvSourceSeeded  = "source.seeded"  // info        | source_id, count

	EvCycleStart       = "poll.cycle.start"    // info        | sources
	EvCycleEnd         = "poll.cycle.end"      // info        | sources, new_items, posted, errors, duration_ms
	EvSourceStart      = "poll.source.start"   // debug       | source_id
	EvSourceOK         = "poll.source.ok"      // info        | source_id, http_status, items, new, posted, duration_ms
	EvSourceError      = "poll.source.error"   // error       | source_id, error, consecutive_failures
	EvSourceRemovedMid = "poll.source.removed" // warn        | source_id

	EvPostSent        = "post.sent"        // info        | source_id, item_key, message_id
	EvPostFailed      = "post.failed"      // error       | source_id, item_key, error, retry_decision
	EvPostRateLimited = "post.ratelimited" // warn        | source_id, item_key, retry_after
	EvPostAbandoned   = "post.abandoned"   // warn        | source_id, item_key
	EvPostUnlinked    = "post.unlinked"    // warn        | source_id, item_key

	EvCommandReceived = "command.received" // info        | command, user_id
	EvCommandRejected = "command.rejected" // warn        | reason, user_id
	EvBotError        = "bot.error"        // error       | error
	EvCacheError      = "cache.error"      // warn        | op, key, error
)
