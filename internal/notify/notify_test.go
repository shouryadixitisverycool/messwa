package notify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/store"
)

type fakeWhatsApp struct {
	mu        sync.Mutex
	connected bool
	selfJID   types.JID
	sendErr   error
	sent      []sentWA
}

type sentWA struct {
	to   types.JID
	text string
}

func (f *fakeWhatsApp) SendSelfText(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return fmt.Errorf("%w: WhatsApp disconnected", ErrChannelUnavailable)
	}
	if f.selfJID.IsEmpty() {
		return fmt.Errorf("%w: WhatsApp not linked", ErrChannelUnavailable)
	}
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentWA{to: f.selfJID.ToNonAD(), text: text})
	return nil
}

func (f *fakeWhatsApp) messages() []sentWA {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentWA(nil), f.sent...)
}

// fakeWebPush simulates a second channel (like Web Push) plugged into
// Dispatcher with zero changes to the dispatcher.
type fakeWebPush struct {
	mu      sync.Mutex
	enabled bool
	batches []Batch
	texts   []string
}

func (p *fakeWebPush) Name() string                { return "web_push" }
func (p *fakeWebPush) Enabled(*config.Config) bool { return p.enabled }
func (p *fakeWebPush) SupportsKind(string) bool    { return true }
func (p *fakeWebPush) Send(_ context.Context, b Batch) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batches = append(p.batches, b)
	return nil
}
func (p *fakeWebPush) SendText(_ context.Context, _ int64, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = append(p.texts, text)
	return nil
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func istTime(t *testing.T, hour, min, sec int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, 11, 20, hour, min, sec, 0, loc)
}

func TestOptInOffSendsNothing(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	if cfg.Alerts.WhatsApp {
		t.Fatal("alerts.whatsapp must default to false")
	}
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}
	now := istTime(t, 12, 0, 0)
	items := []Item{
		{ID: "r1", Kind: "refund", Title: "Refund due", Detail: "Send ₹40 back to Asha.", Meal: "lunch"},
		{ID: "b1", Kind: "blocked", Title: "Sale blocked", Detail: "Portal down", Meal: "lunch"},
	}
	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		BaseURL:     func() string { return "https://mess.example.com" },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))

	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 0 {
		t.Fatalf("expected 0 messages when opt-in is off, got %+v", wa.messages())
	}
}

func TestDedupeAndRestartPersistence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "persist.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}
	now := istTime(t, 13, 0, 0)
	items := []Item{
		{ID: "item-1", Kind: "blocked", Title: "Sale blocked", Detail: "Connection failed", Meal: "lunch", Task: 7},
	}

	newDisp := func(s *store.Store) *Dispatcher {
		d := NewDispatcher(DispatcherConfig{
			UserID:      1,
			Store:       s,
			Config:      func() *config.Config { return cfg },
			Source:      func(context.Context) ([]Item, error) { return items, nil },
			BaseURL:     func() string { return "https://mess.example.com" },
			Now:         func() time.Time { return now },
			BatchWindow: NoBatch,
		})
		d.Register(NewWhatsAppChannel(wa))
		return d
	}

	d1 := newDisp(st)
	if err := d1.Step(ctx); err != nil {
		t.Fatal(err)
	}
	// Second poll in the same process must not re-send.
	now = now.Add(time.Minute)
	if err := d1.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 1 {
		t.Fatalf("sent %d messages before restart, want 1", got)
	}
	if wa.messages()[0].to.String() != "919876543210@s.whatsapp.net" {
		t.Fatalf("sent to %s, want own non-AD JID", wa.messages()[0].to)
	}

	// Simulate full service restart by closing and reopening the SQLite store.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	d2 := newDisp(st2)
	now = now.Add(5 * time.Minute)
	if err := d2.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 1 {
		t.Fatalf("after restart sent %d messages total, want 1 (no re-send)", got)
	}

	// A brand new item after restart IS sent.
	items = append(items, Item{ID: "item-2", Kind: "schedule", Title: "Schedule failed", Detail: "Not registered", Meal: "dinner"})
	if err := d2.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 2 {
		t.Fatalf("after new item sent %d messages, want 2", got)
	}
}

func TestBatchingAndPostCollapse(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}

	now := istTime(t, 12, 15, 0)
	var items []Item
	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		BaseURL:     func() string { return "https://mess.example.com/" },
		Now:         func() time.Time { return now },
		BatchWindow: 15 * time.Second,
	})
	d.Register(NewWhatsAppChannel(wa))

	// First failed group post arrives at t=0.
	items = []Item{
		{ID: "post-a", Kind: "post", Title: "Offer didn't post", Detail: "479", Date: "2026-11-20", Meal: "lunch", Task: 42},
	}
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 0 {
		t.Fatalf("flushed before batch window: %+v", wa.messages())
	}

	// Second failed group post for the same task arrives 5s later.
	now = now.Add(5 * time.Second)
	items = append(items, Item{ID: "post-b", Kind: "post", Title: "Offer didn't post", Detail: "479", Date: "2026-11-20", Meal: "lunch", Task: 42})
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 0 {
		t.Fatalf("flushed too early at +5s: %+v", wa.messages())
	}

	// At +15s from first item, both flush together as one collapsed message.
	now = now.Add(10 * time.Second)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := wa.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 batched message, got %d: %+v", len(msgs), msgs)
	}
	want := "⚠️ Lunch offer didn't post in 2 groups. Fix: https://mess.example.com/#attn-post-a"
	if msgs[0].text != want {
		t.Fatalf("message =\n  %q\nwant:\n  %q", msgs[0].text, want)
	}

	// Item resolved before batch window ends must not be sent.
	now = now.Add(time.Minute)
	items = append(items, Item{ID: "transient", Kind: "blocked", Title: "Sale blocked", Detail: "temp", Meal: "dinner"})
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	items = items[:2] // resolved before flush
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Second)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 1 {
		t.Fatalf("resolved item was still sent: %+v", wa.messages())
	}
}

func TestRateLimitPerHourAndRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ratelimit.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	cfg.Alerts.MaxPerHour = 2
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}

	now := istTime(t, 14, 0, 0)
	var items []Item
	newDisp := func(s *store.Store) *Dispatcher {
		d := NewDispatcher(DispatcherConfig{
			UserID:      1,
			Store:       s,
			Config:      func() *config.Config { return cfg },
			Source:      func(context.Context) ([]Item, error) { return items, nil },
			BaseURL:     func() string { return "https://mess.example.com" },
			Now:         func() time.Time { return now },
			BatchWindow: NoBatch,
		})
		d.Register(NewWhatsAppChannel(wa))
		return d
	}

	d := newDisp(st)
	for i := 1; i <= 3; i++ {
		items = []Item{{ID: fmt.Sprintf("b%d", i), Kind: "blocked", Title: "Sale blocked", Detail: fmt.Sprintf("err %d", i), Meal: "lunch"}}
		if err := d.Step(ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
	}
	if got := len(wa.messages()); got != 2 {
		t.Fatalf("sent %d messages with max_per_hour=2, want 2", got)
	}

	// Reopen DB (simulate restart) within the same hour: cap must still hold.
	st.Close()
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	d2 := newDisp(st2)
	if err := d2.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 2 {
		t.Fatalf("after restart within same hour sent %d, want 2", got)
	}

	// Advance past 1 hour from the first send: item b3 should now go through.
	now = istTime(t, 15, 0, 1)
	if err := d2.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 3 {
		t.Fatalf("after hourly window opened sent %d, want 3", got)
	}
}

func TestQuietHoursAndRefundBypass(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	// Default quiet hours: 00:00-07:00 IST.
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}

	now := istTime(t, 2, 30, 0) // 02:30 IST (inside quiet hours)
	items := []Item{
		{ID: "blk-night", Kind: "blocked", Title: "Sale blocked", Detail: "Portal down", Meal: "breakfast"},
		{ID: "ref-night", Kind: "refund", Title: "Refund due", Detail: "Send ₹45 back to Ravi.", Meal: "dinner"},
	}
	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		BaseURL:     func() string { return "https://mess.example.com" },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))

	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := wa.messages()
	if len(msgs) != 1 {
		t.Fatalf("during quiet hours expected 1 message (refund only), got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].text, "Refund due") || strings.Contains(msgs[0].text, "Sale blocked") {
		t.Fatalf("quiet hours message should contain only refund, got %q", msgs[0].text)
	}

	// Still quiet at 06:59 IST.
	now = istTime(t, 6, 59, 0)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 1 {
		t.Fatalf("sent non-refund at 06:59 during quiet hours: %+v", wa.messages())
	}

	// At 07:00 IST quiet hours end: deferred blocked item is now delivered.
	now = istTime(t, 7, 0, 0)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	msgs = wa.messages()
	if len(msgs) != 2 {
		t.Fatalf("expected deferred blocked item at 07:00, got %d messages", len(msgs))
	}
	if !strings.Contains(msgs[1].text, "sale blocked") {
		t.Fatalf("unexpected morning message: %q", msgs[1].text)
	}

	// Verify overnight wraparound quiet hours (23:00-07:00).
	cfg.Alerts.QuietStart = "23:00"
	cfg.Alerts.QuietEnd = "07:00"
	now = istTime(t, 23, 30, 0)
	items = append(items, Item{ID: "late-sched", Kind: "schedule", Title: "Schedule failed", Detail: "No reg", Meal: "breakfast"})
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 2 {
		t.Fatalf("sent non-refund at 23:30 during overnight quiet hours: %+v", wa.messages())
	}
}

func TestOfflineRecordedAndSecondChannelCovers(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	cfg.Alerts.Kinds = append(cfg.Alerts.Kinds, "whatsapp")

	// WhatsApp is disconnected.
	wa := &fakeWhatsApp{connected: false, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}
	webPush := &fakeWebPush{enabled: true}

	now := istTime(t, 13, 15, 0)
	items := []Item{
		{ID: "ref-1", Kind: "refund", Title: "Refund due", Detail: "Send ₹50 back to Asha.", Meal: "lunch"},
		{ID: "wa-down", Kind: "whatsapp", Title: "WhatsApp disconnected", Account: true},
	}
	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		BaseURL:     func() string { return "https://mess.example.com" },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))
	d.Register(webPush)

	// Poll several times while offline: must not crash, must not retry-storm.
	for range 5 {
		if err := d.Step(ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(15 * time.Second)
	}

	if len(wa.messages()) != 0 {
		t.Fatalf("disconnected WA should send 0 messages, got %d", len(wa.messages()))
	}

	// Verify offline state is persisted for WhatsApp (only once, no retry-storm).
	offlineRows, err := st.AlertDeliveriesByStatus(ctx, 1, store.AlertOffline)
	if err != nil {
		t.Fatal(err)
	}
	if len(offlineRows) != 1 || offlineRows[0].ItemID != "ref-1" || offlineRows[0].Channel != ChannelWhatsApp || offlineRows[0].Attempts != 1 {
		t.Fatalf("offline rows = %+v, want 1 attempt for ref-1 on whatsapp", offlineRows)
	}

	// Verify the second channel (Web Push) covered BOTH ref-1 and wa-down with zero dispatcher changes.
	if len(webPush.batches) != 1 || len(webPush.batches[0].Items) != 2 {
		t.Fatalf("web_push batches = %+v, want 1 batch with both items", webPush.batches)
	}
}

func TestTransientSendErrorBackoffAndCap(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	wa := &fakeWhatsApp{
		connected: true,
		selfJID:   types.NewJID("919876543210", types.DefaultUserServer),
		sendErr:   errors.New("temporary server error"),
	}
	now := istTime(t, 12, 0, 0)
	items := []Item{{ID: "b1", Kind: "blocked", Title: "Sale blocked", Detail: "err", Meal: "lunch"}}

	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))

	// Attempt 1 fails -> backoff 1m.
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	// Polling 10s later should NOT retry (backoff prevents retry-storm).
	now = now.Add(10 * time.Second)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries, err := st.AlertDeliveries(ctx, 1, ChannelWhatsApp)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries["b1"].Status != store.AlertFailed || deliveries["b1"].Attempts != 1 {
		t.Fatalf("after backoff check: %+v", deliveries["b1"])
	}

	// Advance past backoff twice to hit maxFailedAttempts (3).
	now = now.Add(2 * time.Minute)
	_ = d.Step(ctx) // attempt 2
	now = now.Add(10 * time.Minute)
	_ = d.Step(ctx) // attempt 3
	now = now.Add(time.Hour)
	_ = d.Step(ctx) // should be capped

	deliveries, _ = st.AlertDeliveries(ctx, 1, ChannelWhatsApp)
	if deliveries["b1"].Attempts != maxFailedAttempts {
		t.Fatalf("attempts = %d, want capped at %d", deliveries["b1"].Attempts, maxFailedAttempts)
	}
}

func TestFormatBatchPlainWordsNoEmDash(t *testing.T) {
	batch := Batch{
		UserID:  1,
		BaseURL: "https://dash.example.com",
		Items: []Item{
			{ID: "a1", Kind: "refund", Title: "Refund due", Detail: "Send ₹40.00 back to Asha — late payment", Meal: "lunch"},
			{ID: "a2", Kind: "delivery", Title: "Check QR delivery", Detail: "QR may not have reached them.", Meal: "dinner#f1"},
		},
	}
	text := FormatBatch(batch)
	if strings.ContainsAny(text, "\u2014\u2013") {
		t.Fatalf("formatted message contains em/en dash: %q", text)
	}
	if !strings.Contains(text, "https://dash.example.com/#attn-a1") || !strings.Contains(text, "https://dash.example.com/#attn-a2") {
		t.Fatalf("missing attention links in %q", text)
	}
	if strings.Contains(text, "#f1") {
		t.Fatalf("friend slot suffix leaked into message: %q", text)
	}

	// Empty base URL omits broken Fix link.
	noURL := FormatBatch(Batch{Items: []Item{{ID: "x", Kind: "blocked", Meal: "breakfast", Detail: "Portal timeout"}}})
	if strings.Contains(noURL, "Fix:") {
		t.Fatalf("unexpected Fix link when BaseURL is empty: %q", noURL)
	}
}

func TestWeeklyRecapHook(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}
	loc, _ := time.LoadLocation("Asia/Kolkata")
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, loc) // Sunday 20:00 IST

	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return nil, nil },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))

	// Unset recap hook is a no-op.
	if err := d.SendWeeklyRecap(ctx); err != nil {
		t.Fatal(err)
	}
	if len(wa.messages()) != 0 {
		t.Fatal("unset recap hook sent a message")
	}

	// Wiring a recap builder sends once per ISO week.
	d.SetWeeklyRecap(func(_ context.Context, _ int64, _ time.Time) (string, error) {
		return "Weekly recap: 4 meals sold, Rs 180 saved.", nil
	})
	if err := d.SendWeeklyRecap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.SendWeeklyRecap(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 1 {
		t.Fatalf("recap sent %d times in same week, want 1", got)
	}
}

func TestRunRateLimitDoesNotSpin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	cfg.Alerts.MaxPerHour = 1
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}

	// Use an active daytime clock and pre-fill the hourly cap.
	now := istTime(t, 14, 0, 0)
	if err := st.RecordAlertSentBatch(ctx, 1, ChannelWhatsApp, []string{"prev"}, now); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	d := NewDispatcher(DispatcherConfig{
		UserID: 1,
		Store:  st,
		Config: func() *config.Config { return cfg },
		Source: func(context.Context) ([]Item, error) {
			calls.Add(1)
			return []Item{{ID: "b1", Kind: "blocked", Title: "Sale blocked", Detail: "err", Meal: "lunch"}}, nil
		},
		Now:          func() time.Time { return now },
		BatchWindow:  NoBatch,
		PollInterval: 200 * time.Millisecond,
	})
	d.Register(NewWhatsAppChannel(wa))

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()
	time.Sleep(350 * time.Millisecond)
	cancel()
	<-done

	// Expect at most ~3 polls (initial + 1-2 ticks), NOT a 10ms spin loop (~35 calls).
	if got := calls.Load(); got > 5 {
		t.Fatalf("Run spun while rate-limited: %d source calls in 350ms", got)
	}
}

func TestOfflineRetriesOnChannelOnline(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	wa := &fakeWhatsApp{connected: false, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}
	now := istTime(t, 13, 0, 0)
	items := []Item{{ID: "ref-blip", Kind: "refund", Title: "Refund due", Detail: "Send ₹40 back to Asha.", Meal: "lunch"}}

	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))

	// Offline: recorded once, does not retry-storm on subsequent polls.
	for range 3 {
		if err := d.Step(ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(15 * time.Second)
	}
	offlineRows, err := st.AlertDeliveriesByStatus(ctx, 1, store.AlertOffline)
	if err != nil || len(offlineRows) != 1 || offlineRows[0].Attempts != 1 {
		t.Fatalf("offline rows = %+v, err = %v", offlineRows, err)
	}

	// Reconnect: ChannelOnline unlocks the offline item once and delivers it.
	wa.mu.Lock()
	wa.connected = true
	wa.mu.Unlock()
	d.ChannelOnline(ChannelWhatsApp)
	now = now.Add(5 * time.Second)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 1 {
		t.Fatalf("after ChannelOnline sent %d messages, want 1", got)
	}

	// Subsequent poll must not re-send.
	now = now.Add(time.Minute)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 1 {
		t.Fatalf("re-sent after reconnect delivery: %d", got)
	}
}

type failingRecordStore struct {
	DeliveryStore
	failRecord bool
}

func (f *failingRecordStore) RecordAlertSentBatch(ctx context.Context, userID int64, channel string, itemIDs []string, at time.Time) error {
	if f.failRecord {
		return errors.New("sqlite busy")
	}
	return f.DeliveryStore.RecordAlertSentBatch(ctx, userID, channel, itemIDs, at)
}

func TestSendSuccessNotDuplicatedIfStoreWriteFails(t *testing.T) {
	ctx := context.Background()
	st := &failingRecordStore{DeliveryStore: openTestStore(t), failRecord: true}
	cfg := config.Default()
	cfg.Alerts.WhatsApp = true
	wa := &fakeWhatsApp{connected: true, selfJID: types.NewJID("919876543210", types.DefaultUserServer)}
	now := istTime(t, 12, 0, 0)
	items := []Item{{ID: "r-mem", Kind: "refund", Title: "Refund due", Detail: "Send ₹30 back.", Meal: "lunch"}}

	d := NewDispatcher(DispatcherConfig{
		UserID:      1,
		Store:       st,
		Config:      func() *config.Config { return cfg },
		Source:      func(context.Context) ([]Item, error) { return items, nil },
		Now:         func() time.Time { return now },
		BatchWindow: NoBatch,
	})
	d.Register(NewWhatsAppChannel(wa))

	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(wa.messages()); got != 1 {
		t.Fatalf("sent %d times after store write failure, want 1", got)
	}
}
