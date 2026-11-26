package notify

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"messwa/internal/config"
	"messwa/internal/store"
)

const (
	// DefaultBatchWindow groups items that appear close together into one message.
	DefaultBatchWindow = 15 * time.Second
	// DefaultPollInterval is how often Run checks for new attention items when
	// not woken earlier by Wake.
	DefaultPollInterval = 15 * time.Second
	// NoBatch can be passed as DispatcherConfig.BatchWindow to flush immediately
	// without waiting for a batch window.
	NoBatch = -1

	maxFailedAttempts = 3
	sendTimeout       = 30 * time.Second
	commitTimeout     = 2 * time.Second
)

var failedBackoff = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

// DispatcherConfig configures a per-user Dispatcher.
type DispatcherConfig struct {
	UserID       int64
	Store        DeliveryStore
	Config       func() *config.Config
	Source       func(ctx context.Context) ([]Item, error)
	BaseURL      func() string
	Log          *slog.Logger
	Now          func() time.Time
	BatchWindow  time.Duration // 0 uses DefaultBatchWindow; negative (NoBatch) flushes immediately.
	PollInterval time.Duration // <=0 uses DefaultPollInterval.
}

type pendingItem struct {
	item      Item
	firstSeen time.Time
}

// Dispatcher polls a user's attention items, applies dedupe, batching, rate
// limiting, and quiet hours, and hands ready batches to every registered
// Channel. It has no channel-specific logic.
type Dispatcher struct {
	userID       int64
	store        DeliveryStore
	cfgFn        func() *config.Config
	source       func(ctx context.Context) ([]Item, error)
	baseURL      func() string
	log          *slog.Logger
	now          func() time.Time
	batchWindow  time.Duration
	pollInterval time.Duration

	wake chan struct{}

	mu              sync.Mutex
	channels        []Channel
	pending         map[string]map[string]pendingItem
	nextEligible    map[string]time.Time
	sourceBackoff   time.Time
	onlineSeq       map[string]uint64
	offlineTriedSeq map[string]map[string]uint64
	sentMem         map[string]map[string]bool
	recapFn         WeeklyRecapFunc

	// stepMu serializes Step and SendWeeklyRecap so concurrent wakes cannot
	// double-send the same batch.
	stepMu sync.Mutex
}

// NewDispatcher creates a channel-agnostic alert dispatcher for one user.
func NewDispatcher(c DispatcherConfig) *Dispatcher {
	bw := c.BatchWindow
	if bw == 0 {
		bw = DefaultBatchWindow
	} else if bw < 0 {
		bw = 0
	}
	pi := c.PollInterval
	if pi <= 0 {
		pi = DefaultPollInterval
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	log := c.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Dispatcher{
		userID:          c.UserID,
		store:           c.Store,
		cfgFn:           c.Config,
		source:          c.Source,
		baseURL:         c.BaseURL,
		log:             log,
		now:             now,
		batchWindow:     bw,
		pollInterval:    pi,
		wake:            make(chan struct{}, 1),
		pending:         map[string]map[string]pendingItem{},
		nextEligible:    map[string]time.Time{},
		onlineSeq:       map[string]uint64{},
		offlineTriedSeq: map[string]map[string]uint64{},
		sentMem:         map[string]map[string]bool{},
	}
}

// Register adds a notification channel to the dispatcher. Safe to call before
// or after Run starts. Duplicate channel names are ignored.
func (d *Dispatcher) Register(ch Channel) {
	if ch == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, existing := range d.channels {
		if existing.Name() == ch.Name() {
			return
		}
	}
	d.channels = append(d.channels, ch)
	if _, ok := d.pending[ch.Name()]; !ok {
		d.pending[ch.Name()] = map[string]pendingItem{}
	}
}

// Channels returns a snapshot of the registered channels.
func (d *Dispatcher) Channels() []Channel {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Channel(nil), d.channels...)
}

// Wake nudges Run to check attention items immediately instead of waiting for
// the next poll tick. Never blocks.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// ChannelOnline notifies the dispatcher that channelName has (re)connected.
// Items previously recorded as store.AlertOffline for that channel become
// eligible once on this reconnect so brief disconnects or startup races do not
// permanently suppress unresolved alerts, without retry-storming while offline.
func (d *Dispatcher) ChannelOnline(channelName string) {
	d.mu.Lock()
	d.onlineSeq[channelName]++
	delete(d.nextEligible, channelName)
	d.mu.Unlock()
	d.Wake()
}

// Run polls attention items until ctx is cancelled. Errors are logged and never
// crash the loop.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	var flushTimer *time.Timer
	var flushC <-chan time.Time
	resetFlush := func() {
		if flushTimer != nil {
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			flushTimer = nil
			flushC = nil
		}
		if wait, ok := d.nextFlushIn(); ok {
			flushTimer = time.NewTimer(wait)
			flushC = flushTimer.C
		}
	}
	defer func() {
		if flushTimer != nil {
			flushTimer.Stop()
		}
	}()

	runStep := func() {
		if err := d.Step(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("alert dispatch step failed", "user", d.userID, "err", err)
		}
		resetFlush()
	}

	runStep()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runStep()
		case <-d.wake:
			runStep()
		case <-flushC:
			runStep()
		}
	}
}

// nextFlushIn returns how long Run should wait to flush a buffered batch or
// retry a rate-limited channel. It never returns a near-zero spin duration for
// items that are already overdue after the Step that just ran.
func (d *Dispatcher) nextFlushIn() (time.Duration, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if now.Before(d.sourceBackoff) {
		return d.sourceBackoff.Sub(now), true
	}
	var earliest time.Time
	for name, buf := range d.pending {
		if len(buf) == 0 {
			continue
		}
		var oldest time.Time
		for _, p := range buf {
			if oldest.IsZero() || p.firstSeen.Before(oldest) {
				oldest = p.firstSeen
			}
		}
		due := oldest.Add(d.batchWindow)
		if ne := d.nextEligible[name]; ne.After(due) {
			due = ne
		}
		// Only schedule a dedicated flush timer for future due times. Items
		// with due <= now right after Step were held back by an unexpected
		// condition and will be retried on the regular pollInterval ticker.
		if !due.After(now) {
			continue
		}
		if earliest.IsZero() || due.Before(earliest) {
			earliest = due
		}
	}
	if earliest.IsZero() {
		return 0, false
	}
	wait := earliest.Sub(now)
	if wait < 10*time.Millisecond {
		wait = 10 * time.Millisecond
	}
	return wait, true
}

// Step performs one evaluation and dispatch pass across all registered
// channels. Tests can call Step directly with a fake clock for deterministic
// verification of batching, rate limits, quiet hours, and persistence.
func (d *Dispatcher) Step(ctx context.Context) error {
	d.stepMu.Lock()
	defer d.stepMu.Unlock()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d.source == nil || d.cfgFn == nil || d.store == nil {
		return nil
	}
	channels := d.Channels()
	if len(channels) == 0 {
		return nil
	}
	cfg := d.cfgFn()
	if cfg == nil {
		return nil
	}
	// Skip querying the attention source if every registered channel is off.
	anyEnabled := false
	for _, ch := range channels {
		if ch.Enabled(cfg) {
			anyEnabled = true
			break
		}
	}
	if !anyEnabled {
		d.mu.Lock()
		for _, ch := range channels {
			clear(d.pending[ch.Name()])
			delete(d.nextEligible, ch.Name())
		}
		d.mu.Unlock()
		return nil
	}

	now := d.now()
	items, err := d.source(ctx)
	if err != nil {
		d.mu.Lock()
		d.sourceBackoff = now.Add(d.pollInterval)
		d.mu.Unlock()
		return err
	}
	d.mu.Lock()
	d.sourceBackoff = time.Time{}
	d.mu.Unlock()

	current := make(map[string]Item, len(items))
	for _, it := range items {
		if it.ID != "" {
			current[it.ID] = it
		}
	}

	localNow := now
	if loc := cfg.Location(); loc != nil {
		localNow = now.In(loc)
	}
	quiet := cfg.Alerts.InQuietHours(localNow)
	maxPerHour := cfg.Alerts.MaxPerHour
	if maxPerHour <= 0 {
		maxPerHour = config.DefaultAlertsPerHour
	}
	baseURL := ""
	if d.baseURL != nil {
		baseURL = d.baseURL()
	}

	for _, ch := range channels {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.stepChannel(ctx, ch, cfg, current, now, quiet, maxPerHour, baseURL)
	}
	return nil
}

func (d *Dispatcher) stepChannel(
	ctx context.Context,
	ch Channel,
	cfg *config.Config,
	current map[string]Item,
	now time.Time,
	quiet bool,
	maxPerHour int,
	baseURL string,
) {
	name := ch.Name()
	if !ch.Enabled(cfg) {
		d.mu.Lock()
		clear(d.pending[name])
		delete(d.nextEligible, name)
		d.mu.Unlock()
		return
	}

	delivered, err := d.store.AlertDeliveries(ctx, d.userID, name)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("load alert deliveries", "user", d.userID, "channel", name, "err", err)
			d.setNextEligible(name, now.Add(d.pollInterval))
		}
		return
	}

	d.mu.Lock()
	onlineSeq := d.onlineSeq[name]
	triedSeq := d.offlineTriedSeq[name]
	sentMem := d.sentMem[name]
	buf := d.pending[name]
	if buf == nil {
		buf = map[string]pendingItem{}
		d.pending[name] = buf
	}

	// Drop items that were resolved, disabled, already handled, or deferred by
	// quiet hours since the last step.
	for id, p := range buf {
		cur, ok := current[id]
		if !ok || !itemEligible(cur, cfg, ch, quiet, delivered, sentMem, onlineSeq, triedSeq, now) {
			delete(buf, id)
			continue
		}
		p.item = cur
		buf[id] = p
	}

	// Buffer newly eligible items.
	for id, cur := range current {
		if !itemEligible(cur, cfg, ch, quiet, delivered, sentMem, onlineSeq, triedSeq, now) {
			continue
		}
		if _, exists := buf[id]; !exists {
			buf[id] = pendingItem{item: cur, firstSeen: now}
		}
	}

	if len(buf) == 0 {
		delete(d.nextEligible, name)
		d.mu.Unlock()
		return
	}

	var oldest time.Time
	ready := make([]Item, 0, len(buf))
	for _, p := range buf {
		if oldest.IsZero() || p.firstSeen.Before(oldest) {
			oldest = p.firstSeen
		}
		ready = append(ready, p.item)
	}
	d.mu.Unlock()

	if d.batchWindow > 0 && now.Sub(oldest) < d.batchWindow {
		return
	}

	windowStart := now.Add(-time.Hour)
	sentCount, err := d.store.CountAlertSendsSince(ctx, d.userID, name, windowStart)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("count recent alert sends", "user", d.userID, "channel", name, "err", err)
			d.setNextEligible(name, now.Add(d.pollInterval))
		}
		return
	}
	if sentCount >= maxPerHour {
		nextAt := now.Add(d.pollInterval)
		if oldestSend, err := d.store.OldestAlertSendSince(ctx, d.userID, name, windowStart); err == nil && !oldestSend.IsZero() {
			if unlockAt := oldestSend.Add(time.Hour + time.Second); unlockAt.After(now) {
				nextAt = unlockAt
			}
		}
		d.setNextEligible(name, nextAt)
		return
	}
	d.clearNextEligible(name)

	sortItems(ready)
	ids := make([]string, len(ready))
	for i, it := range ready {
		ids[i] = it.ID
	}

	sendCtx, cancelSend := context.WithTimeout(ctx, sendTimeout)
	err = ch.Send(sendCtx, Batch{
		UserID:  d.userID,
		Items:   ready,
		BaseURL: baseURL,
	})
	cancelSend()

	if err != nil && ctx.Err() != nil {
		// Parent context was cancelled during shutdown; do not pollute
		// alert_deliveries with a spurious failed attempt.
		return
	}

	switch {
	case err == nil:
		// Mark sent in memory first so a transient SQLite failure after
		// transport success cannot cause this process to double-send.
		d.markSentMem(name, ids)
		commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
		if recErr := d.store.RecordAlertSentBatch(commitCtx, d.userID, name, ids, now); recErr != nil {
			d.log.Error("record sent alert batch", "user", d.userID, "channel", name, "err", recErr)
		}
		cancelCommit()
		d.clearPending(name, ids)
	case errors.Is(err, ErrChannelUnavailable):
		// Record offline state so a fallback channel (e.g. Web Push) can cover
		// these items, and record onlineSeq so we don't retry-storm until
		// ChannelOnline signals a reconnect.
		d.markOfflineTried(name, ids, onlineSeq)
		if recErr := d.store.RecordAlertOutcome(ctx, d.userID, name, ids, store.AlertOffline, err.Error(), time.Time{}, now); recErr != nil {
			d.log.Error("record offline alert outcome", "user", d.userID, "channel", name, "err", recErr)
		}
		d.clearPending(name, ids)
		d.log.Info("alert channel unavailable; recorded offline", "user", d.userID, "channel", name, "items", len(ids), "reason", err.Error())
	default:
		attempts := 0
		for _, id := range ids {
			if rec, ok := delivered[id]; ok && rec.Attempts > attempts {
				attempts = rec.Attempts
			}
		}
		backoffIdx := attempts
		if backoffIdx >= len(failedBackoff) {
			backoffIdx = len(failedBackoff) - 1
		}
		retryAfter := now.Add(failedBackoff[backoffIdx])
		if recErr := d.store.RecordAlertOutcome(ctx, d.userID, name, ids, store.AlertFailed, err.Error(), retryAfter, now); recErr != nil {
			d.log.Error("record failed alert outcome", "user", d.userID, "channel", name, "err", recErr)
		}
		d.clearPending(name, ids)
		d.log.Warn("alert send failed", "user", d.userID, "channel", name, "items", len(ids), "attempt", attempts+1, "err", err)
	}
}

func (d *Dispatcher) setNextEligible(channel string, at time.Time) {
	d.mu.Lock()
	d.nextEligible[channel] = at
	d.mu.Unlock()
}

func (d *Dispatcher) clearNextEligible(channel string) {
	d.mu.Lock()
	delete(d.nextEligible, channel)
	d.mu.Unlock()
}

func (d *Dispatcher) markSentMem(channel string, ids []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.sentMem[channel]
	if m == nil {
		m = map[string]bool{}
		d.sentMem[channel] = m
	}
	for _, id := range ids {
		m[id] = true
	}
}

func (d *Dispatcher) markOfflineTried(channel string, ids []string, seq uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.offlineTriedSeq[channel]
	if m == nil {
		m = map[string]uint64{}
		d.offlineTriedSeq[channel] = m
	}
	for _, id := range ids {
		m[id] = seq
	}
}

func (d *Dispatcher) clearPending(channel string, ids []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	buf := d.pending[channel]
	for _, id := range ids {
		delete(buf, id)
	}
}

func itemEligible(
	it Item,
	cfg *config.Config,
	ch Channel,
	quiet bool,
	delivered map[string]store.AlertDelivery,
	sentMem map[string]bool,
	onlineSeq uint64,
	triedSeq map[string]uint64,
	now time.Time,
) bool {
	if !cfg.Alerts.AllowsKind(it.Kind) || !ch.SupportsKind(it.Kind) {
		return false
	}
	// Refunds always bypass quiet hours because money needs to be sent back.
	if quiet && it.Kind != "refund" {
		return false
	}
	if sentMem[it.ID] {
		return false
	}
	rec, exists := delivered[it.ID]
	if !exists {
		return true
	}
	switch rec.Status {
	case store.AlertSent:
		return false
	case store.AlertOffline:
		// Do not retry while offline; allow one retry after a subsequent
		// ChannelOnline reconnect event.
		return onlineSeq > 0 && onlineSeq > triedSeq[it.ID]
	case store.AlertFailed:
		if rec.Attempts >= maxFailedAttempts {
			return false
		}
		if !rec.RetryAfter.IsZero() && now.Before(rec.RetryAfter) {
			return false
		}
		return true
	default:
		return true
	}
}

var kindPriority = map[string]int{
	"refund":   0,
	"delivery": 1,
	"blocked":  2,
	"schedule": 3,
	"post":     4,
	"rule":     5,
	"whatsapp": 6,
}

func sortItems(items []Item) {
	slices.SortFunc(items, func(a, b Item) int {
		pa, oka := kindPriority[a.Kind]
		if !oka {
			pa = 99
		}
		pb, okb := kindPriority[b.Kind]
		if !okb {
			pb = 99
		}
		if c := cmp.Compare(pa, pb); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Date, b.Date); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Meal, b.Meal); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Task, b.Task); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}
