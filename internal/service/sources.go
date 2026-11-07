// Package service holds the use-case layer that sits between the bot and
// the poller on one side and storage, cache, and feed on the other. It
// depends on storage, cache, feed, model, render, config, and logging, and
// on nothing above it.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/render"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// ErrBadURL reports a feed URL that failed normalization. It wraps the underlying reason so the reply can show it.
type ErrBadURL struct {
	URL string
	Err error
}

func (e *ErrBadURL) Error() string {
	return fmt.Sprintf("not a usable feed URL: %s", e.Err)
}

func (e *ErrBadURL) Unwrap() error { return e.Err }

// ErrDuplicateSource reports that the normalized URL is already subscribed.
// It carries the existing source's id and title so the reply can name it
// rather than silently no-op.
type ErrDuplicateSource struct {
	FeedURL       string
	ExistingID    int64
	ExistingTitle string
}

func (e *ErrDuplicateSource) Error() string {
	return fmt.Sprintf("already added as #%d: %s", e.ExistingID, e.ExistingTitle)
}

// SourceService implements the add / remove / list / status use cases.
type SourceService struct {
	st      storage.Storage
	seen    *cache.SeenKeys
	list    *cache.SourceList
	fetcher feed.Fetcher
	cfg     config.Config
	log     *slog.Logger
}

// New returns a SourceService over the given collaborators.
func New(st storage.Storage, seen *cache.SeenKeys, list *cache.SourceList, f feed.Fetcher, cfg config.Config, log *slog.Logger) *SourceService {
	if log == nil {
		log = slog.Default()
	}
	return &SourceService{st: st, seen: seen, list: list, fetcher: f, cfg: cfg, log: log}
}

// Add runs the nine steps below in order and returns the persisted source
// and the number of items seeded as already-seen. The feed is
// fetched and parsed before anything is written, so a rejected add leaves
// storage untouched.
func (s *SourceService) Add(ctx context.Context, rawURL, displayName string) (model.Source, int, error) {
	// 1. Normalize.
	norm, err := feed.NormalizeURL(rawURL)
	if err != nil {
		return model.Source{}, 0, &ErrBadURL{URL: rawURL, Err: err}
	}

	// 2. Pre-check duplicate — the fast path for duplicate rejection.
	existing, err := s.st.GetSourceByURL(ctx, norm)
	switch {
	case err == nil:
		return model.Source{}, 0, &ErrDuplicateSource{
			FeedURL:       norm,
			ExistingID:    existing.ID,
			ExistingTitle: existing.Title,
		}
	case !errors.Is(err, storage.ErrNotFound):
		return model.Source{}, 0, err
	}

	// 3. Fetch and parse BEFORE persisting. No validators on a first
	// fetch, so a 304 is impossible. Fetch and parse errors are returned
	// verbatim so the reply carries the real reason.
	res, err := s.fetcher.Fetch(ctx, feed.FetchRequest{URL: norm})
	if err != nil {
		return model.Source{}, 0, err
	}
	if res == nil || res.Feed == nil {
		return model.Source{}, 0, &feed.ParseError{Err: errors.New("empty feed response")}
	}

	// 4. Resolve the display title.
	title := s.resolveTitle(displayName, res.Feed.Title, norm)

	// 5. Seed keys; already intra-feed deduped by the parser.
	seedKeys := make([]string, 0, len(res.Feed.Items))
	for _, it := range res.Feed.Items {
		seedKeys = append(seedKeys, it.Key)
	}

	// 6. Persist atomically: the source row and its seed rows in one call.
	now := time.Now().UTC()
	saved, err := s.st.AddSource(ctx, model.Source{
		FeedURL:       norm,
		Title:         title,
		CreatedAt:     now,
		LastPolledAt:  &now,
		LastSuccessAt: &now,
		ETag:          res.ETag,
		LastModified:  res.LastModified,
	}, seedKeys)
	if err != nil {
		// A lost race with a concurrent add is the same duplicate rejection.
		if errors.Is(err, storage.ErrDuplicate) {
			if raced, qerr := s.st.GetSourceByURL(ctx, norm); qerr == nil {
				return model.Source{}, 0, &ErrDuplicateSource{
					FeedURL:       norm,
					ExistingID:    raced.ID,
					ExistingTitle: raced.Title,
				}
			}
		}
		return model.Source{}, 0, err
	}

	// 7. Warm the cache. Cache failures are logged and never fatal.
	if err := s.list.Invalidate(ctx); err != nil {
		cacheWarn(ctx, s.log, "sources.invalidate", err)
	}
	set := cache.NewKeySet(s.cfg.CacheSeenPerSrc)
	set.Add(seedKeys...)
	if err := s.seen.Store(ctx, saved.ID, set); err != nil {
		cacheWarn(ctx, s.log, "seen.store", err)
	}

	// 8. Log.
	s.log.InfoContext(ctx, logging.EvSourceAdded,
		"source_id", saved.ID, "feed_url", saved.FeedURL, "title", saved.Title)
	s.log.InfoContext(ctx, logging.EvSourceSeeded,
		"source_id", saved.ID, "count", len(seedKeys))

	// 9. Return.
	return saved, len(seedKeys), nil
}

// Remove deletes the source identified by arg, which is either a numeric id
// or a feed URL. Both cache views are invalidated.
func (s *SourceService) Remove(ctx context.Context, arg string) (model.Source, error) {
	var (
		src model.Source
		err error
	)
	if id, perr := strconv.ParseInt(strings.TrimSpace(arg), 10, 64); perr == nil {
		src, err = s.st.GetSourceByID(ctx, id)
	} else {
		norm, nerr := feed.NormalizeURL(arg)
		if nerr != nil {
			return model.Source{}, &ErrBadURL{URL: arg, Err: nerr}
		}
		src, err = s.st.GetSourceByURL(ctx, norm)
	}
	if err != nil {
		return model.Source{}, err
	}

	if err := s.st.DeleteSource(ctx, src.ID); err != nil {
		return model.Source{}, err
	}

	if err := s.seen.Invalidate(ctx, src.ID); err != nil {
		cacheWarn(ctx, s.log, "seen.invalidate", err)
	}
	if err := s.list.Invalidate(ctx); err != nil {
		cacheWarn(ctx, s.log, "sources.invalidate", err)
	}

	s.log.InfoContext(ctx, logging.EvSourceRemoved, "source_id", src.ID, "feed_url", src.FeedURL)
	return src, nil
}

// List returns every source, preferring the cached view. A cache miss or a
// cache error always falls through to storage.
func (s *SourceService) List(ctx context.Context) ([]model.Source, error) {
	cached, found, err := s.list.Load(ctx)
	if err != nil {
		cacheWarn(ctx, s.log, "sources.load", err)
	} else if found {
		return cached, nil
	}

	ss, err := s.st.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.list.Store(ctx, ss); err != nil {
		cacheWarn(ctx, s.log, "sources.store", err)
	}
	return ss, nil
}

// Status fills the storage-derived fields of the /status payload. The
// cycle-related fields come from the poller snapshot passed in as cycle.
func (s *SourceService) Status(ctx context.Context, startedAt time.Time, cycle model.Status) (model.Status, error) {
	out := cycle
	out.StartedAt = startedAt

	ss, err := s.st.ListSources(ctx)
	if err != nil {
		return model.Status{}, err
	}
	out.SourceCount = len(ss)

	posted, err := s.st.CountPostedSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return model.Status{}, err
	}
	out.PostedLast24h = posted

	return out, nil
}

// resolveTitle picks the display title: the user's override if given, else the
// feed's own title, cleaned and truncated to cfg.SourceTitleMax runes, with
// the URL host as the fallback so the title is never empty.
func (s *SourceService) resolveTitle(displayName, feedTitle, normURL string) string {
	max := s.cfg.SourceTitleMax
	if max <= 0 {
		max = 64
	}

	raw := displayName
	if strings.TrimSpace(raw) == "" {
		raw = feedTitle
	}

	title := truncateRunes(render.CleanText(raw), max)
	if title != "" {
		return title
	}
	if u, err := url.Parse(normURL); err == nil {
		return truncateRunes(u.Host, max)
	}
	return truncateRunes(normURL, max)
}

// truncateRunes cuts s to at most max runes, always on a rune boundary.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// cacheWarn logs a cache failure at warn and returns. No caller ever changes
// behavior because of one.
func cacheWarn(ctx context.Context, log *slog.Logger, op string, err error) {
	log.WarnContext(ctx, logging.EvCacheError, "op", op, "error", err.Error())
}
