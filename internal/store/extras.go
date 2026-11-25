package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExtraSlotPrefix marks a separately sold extra registration's slot:
// "dinner" + "#e" + n (e.g. "dinner#e1", "dinner#e2"). Each slot sells one
// extra item independently of the main meal and pools with other slots for the
// same extra on the same date and meal.
const ExtraSlotPrefix = "#e"

// IsExtraSlot reports whether a meal slot name is a separately sold extra slot.
func IsExtraSlot(meal string) bool {
	_, suffix, ok := strings.Cut(meal, ExtraSlotPrefix)
	if !ok || suffix == "" {
		return false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false
		}
	}
	return true
}

// ExtraSlot builds the nth extra slot name for baseMeal ("dinner" -> "dinner#e1").
func ExtraSlot(baseMeal string, n int) string {
	return fmt.Sprintf("%s%s%d", baseMeal, ExtraSlotPrefix, n)
}

// BaseMeal strips any slot suffix ("dinner#e1" -> "dinner", "dinner#f1" -> "dinner").
func BaseMeal(meal string) string {
	base, _, _ := strings.Cut(meal, "#")
	return base
}

// ExtraSlotUsage summarizes how many slots for one (date, baseMeal, extra) are
// already committed in the store, partitioned strictly by slot so a slot
// transitioning between schedule, task, and delivery is never dropped or
// double-counted.
type ExtraSlotUsage struct {
	// Sold counts deliveries in sent, sending, or handed_off state.
	Sold int
	// SoldSentAt holds the QR send timestamp for each sent delivery (zero for
	// sending/handed_off deliveries without a recorded sent_at), used to
	// reconcile portal availed_at timestamps.
	SoldSentAt []time.Time
	// Armed counts unpaid deliveries currently in armed state.
	Armed int
	// Active counts running sell tasks on slots that have no live delivery row.
	Active int
	// Scheduled counts waiting or starting schedules on slots that have no
	// live delivery row or active task.
	Scheduled int
}

// ExtraSlotUsage returns a single-transaction snapshot of how many slots for
// extra (case-insensitive) on date and baseMeal are sold, armed, actively
// selling, or scheduled. If excludeSlot is non-empty, that slot is omitted.
func (s *Store) ExtraSlotUsage(ctx context.Context, date, baseMeal, extra, excludeSlot string) (ExtraSlotUsage, error) {
	var u ExtraSlotUsage
	prefix := baseMeal + ExtraSlotPrefix + "%"
	want := strings.ToLower(strings.TrimSpace(extra))
	if want == "" {
		return u, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return u, err
	}
	defer tx.Rollback()

	seen := map[string]bool{}
	if excludeSlot != "" {
		seen[excludeSlot] = true
	}

	drows, err := tx.QueryContext(ctx, `SELECT meal, state, armed_at, sent_at FROM qr_deliveries
 WHERE date=? AND meal LIKE ? AND lower(extra)=? AND state IN ('sent','sending','armed','handed_off')`,
		date, prefix, want)
	if err != nil {
		return u, err
	}
	for drows.Next() {
		var meal, state string
		var armedAt, sentAt sql.NullInt64
		if err := drows.Scan(&meal, &state, &armedAt, &sentAt); err != nil {
			drows.Close()
			return u, err
		}
		if seen[meal] {
			continue
		}
		seen[meal] = true
		if state == "armed" {
			u.Armed++
			continue
		}
		u.Sold++
		// Only record an actual sent_at timestamp; sending/handed_off deliveries
		// without a recorded sent_at stay zero so AvailableExtras never credits
		// an owner scan before QR delivery to an unsent buyer QR.
		var ts time.Time
		if state == "sent" && sentAt.Valid && sentAt.Int64 > 0 {
			ts = time.Unix(sentAt.Int64, 0)
		}
		u.SoldSentAt = append(u.SoldSentAt, ts)
	}
	if err := drows.Close(); err != nil {
		return u, err
	}

	trows, err := tx.QueryContext(ctx, `SELECT meal FROM sell_tasks
 WHERE date=? AND meal LIKE ? AND lower(extra)=? AND state='active'`,
		date, prefix, want)
	if err != nil {
		return u, err
	}
	for trows.Next() {
		var meal string
		if err := trows.Scan(&meal); err != nil {
			trows.Close()
			return u, err
		}
		if seen[meal] {
			continue
		}
		seen[meal] = true
		u.Active++
	}
	if err := trows.Close(); err != nil {
		return u, err
	}

	srows, err := tx.QueryContext(ctx, `SELECT meal FROM sell_schedules
 WHERE date=? AND meal LIKE ? AND lower(extra)=? AND state IN ('waiting','starting')`,
		date, prefix, want)
	if err != nil {
		return u, err
	}
	for srows.Next() {
		var meal string
		if err := srows.Scan(&meal); err != nil {
			srows.Close()
			return u, err
		}
		if seen[meal] {
			continue
		}
		seen[meal] = true
		u.Scheduled++
	}
	if err := srows.Close(); err != nil {
		return u, err
	}
	return u, nil
}

// AllocatedExtras returns, per lowercase extra name, a single-transaction
// snapshot of slot usage across deliveries, active tasks, and waiting/starting
// schedules for date and baseMeal.
func (s *Store) AllocatedExtras(ctx context.Context, date, baseMeal string) (map[string]ExtraSlotUsage, error) {
	out := map[string]ExtraSlotUsage{}
	prefix := baseMeal + ExtraSlotPrefix + "%"
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	seen := map[string]bool{}

	drows, err := tx.QueryContext(ctx, `SELECT meal, lower(extra), state, armed_at, sent_at FROM qr_deliveries
 WHERE date=? AND meal LIKE ? AND extra!='' AND state IN ('sent','sending','armed','handed_off')`, date, prefix)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var meal, name, state string
		var armedAt, sentAt sql.NullInt64
		if err := drows.Scan(&meal, &name, &state, &armedAt, &sentAt); err != nil {
			drows.Close()
			return nil, err
		}
		if name == "" || seen[meal] {
			continue
		}
		seen[meal] = true
		u := out[name]
		if state == "armed" {
			u.Armed++
		} else {
			u.Sold++
			var ts time.Time
			if state == "sent" && sentAt.Valid && sentAt.Int64 > 0 {
				ts = time.Unix(sentAt.Int64, 0)
			}
			u.SoldSentAt = append(u.SoldSentAt, ts)
		}
		out[name] = u
	}
	if err := drows.Close(); err != nil {
		return nil, err
	}

	trows, err := tx.QueryContext(ctx, `SELECT meal, lower(extra) FROM sell_tasks
 WHERE date=? AND meal LIKE ? AND extra!='' AND state='active'`, date, prefix)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var meal, name string
		if err := trows.Scan(&meal, &name); err != nil {
			trows.Close()
			return nil, err
		}
		if name == "" || seen[meal] {
			continue
		}
		seen[meal] = true
		u := out[name]
		u.Active++
		out[name] = u
	}
	if err := trows.Close(); err != nil {
		return nil, err
	}

	srows, err := tx.QueryContext(ctx, `SELECT meal, lower(extra) FROM sell_schedules
 WHERE date=? AND meal LIKE ? AND extra!='' AND state IN ('waiting','starting')`, date, prefix)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var meal, name string
		if err := srows.Scan(&meal, &name); err != nil {
			srows.Close()
			return nil, err
		}
		if name == "" || seen[meal] {
			continue
		}
		seen[meal] = true
		u := out[name]
		u.Scheduled++
		out[name] = u
	}
	if err := srows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

// FreeExtraSlots finds count unused "baseMeal#eN" slot names on date that have
// no delivery record, active task, or waiting/starting schedule.
func (s *Store) FreeExtraSlots(ctx context.Context, date, baseMeal string, count int) ([]string, error) {
	if date == "" || baseMeal == "" || strings.Contains(baseMeal, "#") || count < 1 {
		return nil, errors.New("extra slots need a date, meal and positive count")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	used := map[string]bool{}
	prefix := baseMeal + ExtraSlotPrefix + "%"
	for _, q := range []string{
		`SELECT meal FROM qr_deliveries WHERE date=? AND meal LIKE ?`,
		`SELECT meal FROM sell_tasks WHERE date=? AND meal LIKE ? AND state='active'`,
		`SELECT meal FROM sell_schedules WHERE date=? AND meal LIKE ? AND state IN ('waiting','starting')`,
	} {
		rows, err := tx.QueryContext(ctx, q, date, prefix)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				rows.Close()
				return nil, err
			}
			used[m] = true
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, count)
	for n := 1; n <= 99 && len(out) < count; n++ {
		slot := ExtraSlot(baseMeal, n)
		if !used[slot] {
			out = append(out, slot)
		}
	}
	if len(out) < count {
		return nil, errors.New("too many extra slots for this meal")
	}
	return out, nil
}

// MainMealPromisedExtra reports whether a main-meal task on (date, baseMeal)
// advertised extraName in a group post that still stands ("+ extras: ...").
// Sold and handed-off tasks count with any such post. Active tasks count once
// a buyer has replied, since editing the post can't take back what they were
// offered, or with posts made before editableAfter, which can no longer be
// edited to drop the extra. When true, the extra was promised with the main
// meal and must not be sold separately.
func (s *Store) MainMealPromisedExtra(ctx context.Context, date, baseMeal, extraName string, editableAfter time.Time) (bool, error) {
	want := strings.ToLower(strings.Join(strings.Fields(extraName), " "))
	if want == "" {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.text FROM sell_task_offers o
 JOIN sell_tasks t ON t.id=o.task_id
 WHERE t.date=? AND t.meal=? AND t.extra='' AND o.state=? AND o.revoked_at=0
 AND (t.state IN (?,?) OR (t.state=? AND (o.posted_at<? OR EXISTS (SELECT 1 FROM sell_task_buyers b WHERE b.task_id=t.id))))`,
		date, baseMeal, OfferPosted, TaskSold, TaskHandedOff, TaskActive, editableAfter.Unix())
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return false, err
		}
		_, after, ok := strings.Cut(strings.ToLower(text), "+ extras:")
		if !ok {
			continue
		}
		for _, part := range strings.Split(after, ",") {
			if strings.Join(strings.Fields(part), " ") == want {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}
