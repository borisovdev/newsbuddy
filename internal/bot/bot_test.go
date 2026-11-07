package bot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/borisovdev/newsbuddy/internal/bot"
	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed/feedtest"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/poller"
	"github.com/borisovdev/newsbuddy/internal/service"
	"github.com/borisovdev/newsbuddy/internal/storage/memstore"
)

const (
	ownerID     = int64(4242)
	chatID      = int64(99)
	secretToken = "123456:AAAA-this-token-must-never-appear-in-a-reply"
)

// ---- log sink --------------------------------------------------------

// safeBuf is a mutex-guarded log sink.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// countEvent returns how many JSON log lines carry msg == event.
func (s *safeBuf) countEvent(event string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(s.String()), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == event {
			n++
		}
	}
	return n
}

// ---- reply sink ------------------------------------------------------

// capture records the text of every sendMessage call the bot makes. It is
// the reply-inspection mechanism for every assertion below.
type capture struct {
	mu    sync.Mutex
	texts []string
}

func (c *capture) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, s)
}

func (c *capture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

func (c *capture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = nil
}

// ---- harness ---------------------------------------------------------

type harness struct {
	t     *testing.T
	b     *tgbot.Bot
	cap   *capture
	logs  *safeBuf
	fetch *feedtest.FakeFetcher
	st    *memstore.Store

	nextUpdateID int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parsing sendMessage form: %v", err)
			} else {
				cap.add(r.FormValue("text"))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Defaults()
	cfg.TelegramOwnerID = ownerID
	cfg.PollInterval = time.Hour
	// A recognizable secret, so "no config value ever reaches a
	// reply" is assertable rather than merely asserted.
	cfg.TelegramBotToken = secretToken
	cfg.TelegramChannelID = "@secret-channel"

	logs := &safeBuf{}
	log := logging.New("json", slog.LevelDebug, logs)

	st := memstore.New()
	mem := cache.NewMemory(cfg.CacheEntries)
	seenView := cache.NewSeenKeys(mem, cfg.CacheSeenTTL, cfg.CacheSeenPerSrc)
	listView := cache.NewSourceList(mem, cfg.CacheSourcesTTL)
	ff := feedtest.NewFakeFetcher()
	svc := service.New(st, seenView, listView, ff, cfg, log)
	idx := service.NewSeenIndex(st, seenView, log)
	// Constructed, never run: /status only reads its Snapshot.
	plr := poller.New(svc, idx, st, ff, nil, cfg, log)

	h := bot.NewHandlers(svc, plr, cfg, log)

	// Built exactly the way the composition root builds it, plus the two
	// test-only options that point the client at the httptest server and
	// skip the startup getMe, and WithNotAsyncHandlers so ProcessUpdate is
	// synchronous and the assertions need no polling.
	b, err := tgbot.New("test-token", append(bot.Options(h),
		tgbot.WithServerURL(srv.URL),
		tgbot.WithSkipGetMe(),
		tgbot.WithNotAsyncHandlers(),
	)...)
	if err != nil {
		t.Fatalf("tgbot.New: %v", err)
	}

	return &harness{t: t, b: b, cap: cap, logs: logs, fetch: ff, st: st}
}

// update builds a synthetic command update from userID in the given chat.
func (h *harness) update(userID int64, chatType models.ChatType, text string) *models.Update {
	h.nextUpdateID++
	u := &models.Update{
		ID: h.nextUpdateID,
		Message: &models.Message{
			ID:   int(h.nextUpdateID),
			Text: text,
			Chat: models.Chat{ID: chatID, Type: chatType},
			From: &models.User{ID: userID},
		},
	}
	if strings.HasPrefix(text, "/") {
		cmd := text
		if i := strings.IndexByte(text, ' '); i >= 0 {
			cmd = text[:i]
		}
		u.Message.Entities = []models.MessageEntity{{
			Type:   models.MessageEntityTypeBotCommand,
			Offset: 0,
			Length: len(cmd),
		}}
	}
	return u
}

// send feeds one owner command into the bot's update dispatcher and returns
// the replies it produced.
func (h *harness) send(text string) []string {
	h.t.Helper()
	h.cap.reset()
	h.b.ProcessUpdate(context.Background(), h.update(ownerID, models.ChatTypePrivate, text))
	return h.cap.all()
}

// sendRaw dispatches an arbitrary update without resetting the reply sink.
func (h *harness) sendRaw(u *models.Update) {
	h.b.ProcessUpdate(context.Background(), u)
}

// only asserts exactly one reply was sent and returns it.
func (h *harness) only(replies []string) string {
	h.t.Helper()
	if len(replies) != 1 {
		h.t.Fatalf("got %d replies, want 1: %q", len(replies), replies)
	}
	return replies[0]
}

// ---- middleware ------------------------------------------------------

// A command from a non-owner produces zero replies and exactly one
// command.rejected warn line.
func TestOwnerOnly_RejectsNonOwner(t *testing.T) {
	h := newHarness(t)

	h.sendRaw(h.update(ownerID+1, models.ChatTypePrivate, "/list"))

	if got := h.cap.all(); len(got) != 0 {
		t.Errorf("replies = %q, want none", got)
	}
	if n := h.logs.countEvent(logging.EvCommandRejected); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvCommandRejected, n)
	}
	if !strings.Contains(h.logs.String(), `"user_id":4243`) {
		t.Errorf("rejected line does not carry the sender's user id: %s", h.logs.String())
	}
	if n := h.logs.countEvent(logging.EvCommandReceived); n != 0 {
		t.Errorf("%s lines = %d, want 0", logging.EvCommandReceived, n)
	}
}

// Commands sent anywhere but the bot's private chat are dropped
// silently — no reply and no rejection log line.
func TestOwnerOnly_DropsNonPrivateChat(t *testing.T) {
	h := newHarness(t)

	h.sendRaw(h.update(ownerID, models.ChatTypeChannel, "/list"))

	if got := h.cap.all(); len(got) != 0 {
		t.Errorf("replies = %q, want none", got)
	}
	if n := h.logs.countEvent(logging.EvCommandRejected); n != 0 {
		t.Errorf("%s lines = %d, want 0 (silent drop)", logging.EvCommandRejected, n)
	}
}

// An update with no Message (channel post, edited message, callback) is
// dropped without a reply and without a log line.
func TestOwnerOnly_DropsUpdateWithoutMessage(t *testing.T) {
	h := newHarness(t)

	h.sendRaw(&models.Update{ID: 1})

	if got := h.cap.all(); len(got) != 0 {
		t.Errorf("replies = %q, want none", got)
	}
	if got := strings.TrimSpace(h.logs.String()); got != "" {
		t.Errorf("logs = %s, want silence", got)
	}
}

func TestOwnerOnly_LogsCommandReceived(t *testing.T) {
	h := newHarness(t)

	h.send("/list")

	if n := h.logs.countEvent(logging.EvCommandReceived); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvCommandReceived, n)
	}
	if !strings.Contains(h.logs.String(), `"command":"/list"`) {
		t.Errorf("received line does not carry the command: %s", h.logs.String())
	}
}

// An unknown command gets a usage hint, never silence.
func TestUnknownCommand_RepliesWithUsage(t *testing.T) {
	h := newHarness(t)

	reply := h.only(h.send("/frobnicate"))

	if !strings.Contains(reply, "Unknown command") {
		t.Errorf("reply = %q, want an unknown-command notice", reply)
	}
	for _, syntax := range []string{bot.SyntaxAdd, bot.SyntaxList, bot.SyntaxRemove, bot.SyntaxStatus} {
		if !strings.Contains(reply, syntax) {
			t.Errorf("reply = %q, missing usage %q", reply, syntax)
		}
	}
}

// Options must not construct anything itself; this documents that the shared
// *tgbot.Bot is the caller's to build and to Start.
func TestOptions_ReturnsFullOptionList(t *testing.T) {
	h := bot.NewHandlers(nil, nil, config.Defaults(), slog.Default())
	// middlewares + default handler + 6 commands + errors handler
	if got, want := len(bot.Options(h)), 9; got != want {
		t.Errorf("len(Options) = %d, want %d", got, want)
	}
}
