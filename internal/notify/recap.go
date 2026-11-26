package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"messwa/internal/config"
	"messwa/internal/store"
)

// WeeklyRecapFunc builds the Sunday weekly recap text for a user for the week
// ending at now. Returning an empty string skips sending for that week.
//
// TODO(money-tab): Implement this using the Money tab's weekly earnings and
// savings summary, and wire it into each account's Dispatcher via
// SetWeeklyRecap plus a Sunday schedule.
type WeeklyRecapFunc func(ctx context.Context, userID int64, now time.Time) (string, error)

// SetWeeklyRecap installs the builder used by SendWeeklyRecap.
func (d *Dispatcher) SetWeeklyRecap(fn WeeklyRecapFunc) {
	d.mu.Lock()
	d.recapFn = fn
	d.mu.Unlock()
}

// SendWeeklyRecap builds and sends the user's weekly recap at most once per
// ISO week per enabled channel implementing TextSender. It is a no-op until a
// WeeklyRecapFunc is installed via SetWeeklyRecap.
//
// TODO(money-tab): Call SendWeeklyRecap on Sunday evenings once the Money tab
// supplies the recap content.
func (d *Dispatcher) SendWeeklyRecap(ctx context.Context) error {
	d.stepMu.Lock()
	defer d.stepMu.Unlock()

	d.mu.Lock()
	fn := d.recapFn
	d.mu.Unlock()
	if fn == nil || d.cfgFn == nil || d.store == nil {
		return nil
	}
	cfg := d.cfgFn()
	if cfg == nil {
		return nil
	}

	now := d.now()
	localNow := now
	if loc := cfg.Location(); loc != nil {
		localNow = now.In(loc)
	}
	if cfg.Alerts.InQuietHours(localNow) {
		return nil
	}

	channels := d.Channels()
	var targets []struct {
		ch Channel
		tx TextSender
	}
	for _, ch := range channels {
		if !ch.Enabled(cfg) {
			continue
		}
		if tx, ok := ch.(TextSender); ok {
			targets = append(targets, struct {
				ch Channel
				tx TextSender
			}{ch: ch, tx: tx})
		}
	}
	if len(targets) == 0 {
		return nil
	}

	year, week := localNow.ISOWeek()
	itemID := fmt.Sprintf("recap:%04d-W%02d", year, week)

	maxPerHour := cfg.Alerts.MaxPerHour
	if maxPerHour <= 0 {
		maxPerHour = config.DefaultAlertsPerHour
	}

	var text string
	var built bool
	for _, t := range targets {
		name := t.ch.Name()
		d.flushUnsavedSent(ctx, name)
		d.mu.Lock()
		alreadySentMem := d.sentMem[name][itemID]
		d.mu.Unlock()
		if alreadySentMem {
			continue
		}
		delivered, err := d.store.AlertDeliveriesForItems(ctx, d.userID, name, []string{itemID})
		if err != nil {
			d.log.Warn("load recap deliveries", "user", d.userID, "channel", name, "err", err)
			continue
		}
		if rec, ok := delivered[itemID]; ok && (rec.Status == store.AlertSent || rec.Status == store.AlertOffline) {
			continue
		}
		windowStart := now.Add(-time.Hour)
		sentCount, err := d.store.CountAlertSendsSince(ctx, d.userID, name, windowStart)
		if err != nil {
			continue
		}
		unsavedCount, _ := d.unsavedSendsSince(name, windowStart)
		if sentCount+unsavedCount >= maxPerHour {
			continue
		}
		if !built {
			text, err = fn(ctx, d.userID, localNow)
			if err != nil {
				return err
			}
			built = true
			if text == "" {
				return nil
			}
		}
		sendCtx, cancelSend := context.WithTimeout(ctx, sendTimeout)
		err = t.tx.SendText(sendCtx, d.userID, text)
		cancelSend()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrChannelUnavailable) {
				_ = d.store.RecordAlertOutcome(ctx, d.userID, name, []string{itemID}, store.AlertOffline, err.Error(), time.Time{}, now)
				continue
			}
			d.log.Warn("send weekly recap failed", "user", d.userID, "channel", name, "err", err)
			continue
		}
		d.recordSentWithFallback(ctx, name, []string{itemID}, now)
	}
	return nil
}
