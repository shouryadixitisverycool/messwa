package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"messwa/internal/config"
	"messwa/internal/meals"
	"messwa/internal/store"
)

// Scheduled sales. Selling a meal that hasn't started yet doesn't post
// anything straight away: it saves a schedule, and at [sell] list_at
// around the meal's start a timer starts the sell task as if you had
// clicked then. The task runs till the meal ends, like any other.
//
// No offer goes up before the meal served before it has ended, scheduled
// or started by hand: a dinner offer posted during snacks gets read as a
// snacks offer.
//
// Everything a click would check (the registration, the groups, a sale
// already running) is checked when you schedule, so a mistake shows then,
// and again when the task starts. A waiting schedule has no sell task row,
// so it never joins a pool or changes a pool's prices.

// StartOrSchedule starts a sell task for the meal now when it is being
// served or its list_at time has passed. Otherwise it schedules one and
// returns the schedule's id and when the task will start.
func (m *sellManager) StartOrSchedule(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, autopilot bool) (int64, time.Time, error) {
	return m.PlanSchedule(ctx, userID, 0, 0, date, meal, minRupees, maxRupees, time.Time{}, autopilot)
}

// PlanSchedule creates or edits one waiting sale. An explicit posting time
// is frozen into this schedule, independently of later defaults.
func (m *sellManager) PlanSchedule(ctx context.Context, userID, id, revision int64, date, meal string, minRupees, maxRupees int, at time.Time, autopilot bool) (int64, time.Time, error) {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return 0, time.Time{}, errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	slot, err := findSlot(m.cfg, date, meal)
	if err != nil {
		return 0, time.Time{}, err
	}
	now := time.Now()
	opens := listOpens(m.cfg, slot)
	custom := !at.IsZero()
	if !custom {
		at = listTime(slot.Start, slot.End, m.cfg.Live().Sell.ListAt.Duration, opens)
	}
	if m.cfg.Live().HiddenMeal(meal) {
		return 0, time.Time{}, errors.New("this meal is hidden in settings")
	}
	if custom && (at.Before(slot.Start.Add(-30*time.Minute)) || at.After(slot.Start.Add(90*time.Minute)) || !at.Before(slot.End)) {
		return 0, time.Time{}, errors.New("posting time must be within 30 minutes before and 90 minutes after serving starts, and before the meal ends")
	}
	// A custom time inside the meal before waits for it to end, so the
	// saved time is the one the offer really goes up at.
	if at.Before(opens) {
		at = opens
	}
	if id > 0 && !now.Before(at) {
		return 0, time.Time{}, errors.New("posting time is already due; use Start now or choose a later time")
	}
	if id == 0 && ((!custom && !now.Before(slot.Start)) || !now.Before(at)) {
		id, err := m.start(ctx, userID, date, meal, minRupees, maxRupees, "", autopilot)
		return id, time.Time{}, err
	}
	return m.schedule(ctx, userID, id, revision, slot, date, meal, minRupees, maxRupees, autopilot, at, false)
}

// schedule saves a waiting schedule that starts the meal's task at at, or
// edits schedule id at revision. With replace, it first ends a schedule
// already waiting for the meal.
func (m *sellManager) schedule(ctx context.Context, userID, id, revision int64, slot meals.Slot, date, meal string, minRupees, maxRupees int, autopilot bool, at time.Time, replace bool) (int64, time.Time, error) {
	now := time.Now()
	if settings, err := taskSellSettings(m.cfg); err != nil {
		return 0, time.Time{}, err
	} else if len(settings.Groups) == 0 {
		return 0, time.Time{}, errors.New("no group to post the offer in; add the mess groups to config.toml")
	}
	// The registration has to be there now; the task checks it again
	// when it starts.
	p, err := m.portalFor(ctx, userID)
	if err != nil {
		return 0, time.Time{}, err
	}
	if err := m.newSale(date, meal, slot.End).usePortal(ctx, p, m.cfg.VegOnly); err != nil {
		return 0, time.Time{}, err
	}
	if replace {
		if old, err := m.st.EndWaitingSellSchedule(ctx, date, meal, store.ScheduleCancelled, 0, "replaced", now); err != nil {
			return 0, time.Time{}, err
		} else if old != 0 {
			m.disarm(old)
		}
	}
	sc := store.SellSchedule{ID: id, Revision: revision, Date: date, Meal: meal, MinPaise: int64(minRupees) * 100, MaxPaise: int64(maxRupees) * 100,
		UserID: userID, StartsAt: at, EndsAt: slot.End, CreatedAt: now, Autopilot: autopilot}
	if id > 0 {
		old, e := m.st.GetSellSchedule(ctx, id)
		if e != nil {
			return 0, time.Time{}, e
		}
		if old == nil || old.UserID != userID || old.Date != date || old.Meal != meal {
			return 0, time.Time{}, errors.New("schedule not found for this meal")
		}
		ok, e := m.st.UpdateSellSchedule(ctx, sc)
		if e != nil {
			return 0, time.Time{}, e
		}
		if !ok {
			return 0, time.Time{}, errors.New("schedule changed or started in another tab; refresh and review this meal")
		}
	} else if sc.ID, err = m.st.CreateSellSchedule(ctx, sc); err != nil {
		return 0, time.Time{}, err
	}
	sc.State = store.ScheduleWaiting
	m.arm(sc)
	m.log.Info("sell task scheduled", "schedule", sc.ID, "date", date, "meal", meal, "at", at, "min", minRupees, "max", maxRupees)
	m.changed()
	return sc.ID, at, nil
}

// listTime is when a scheduled sale for a meal from start to end goes up:
// offset from the start, or the start itself when the offset would land
// at or after the end, but never before opens.
func listTime(start, end time.Time, offset time.Duration, opens time.Time) time.Time {
	at := start.Add(offset)
	if !at.Before(end) {
		at = start
	}
	if at.Before(opens) {
		return opens
	}
	return at
}

// listOpens is the earliest an offer for slot may go up: when the meal
// before it ends. Zero when there is none.
func listOpens(cfg *config.Config, slot meals.Slot) time.Time {
	if prev, ok := prevMeal(cfg, slot); ok {
		return prev.End
	}
	return time.Time{}
}

// prevMeal is the meal served before slot, skipping meals the settings
// hide: with snacks hidden, dinner's offer can go up once lunch ends.
func prevMeal(cfg *config.Config, slot meals.Slot) (meals.Slot, bool) {
	return meals.New(cfg).Without(cfg.Live().HiddenMeal).Prev(slot)
}

// tooEarly is the error for starting slot's offer before listOpens.
func tooEarly(cfg *config.Config, slot meals.Slot, now time.Time) error {
	prev, ok := prevMeal(cfg, slot)
	if !ok || !now.Before(prev.End) {
		return nil
	}
	return fmt.Errorf("the %s offer can go up once %s ends at %s; schedule it instead", slot.Meal, prev.Meal, prev.End.In(cfg.Location()).Format("15:04"))
}

// CancelSchedule drops a waiting schedule before its task starts.
func (m *sellManager) CancelSchedule(ctx context.Context, id int64) error {
	sc, err := m.st.GetSellSchedule(ctx, id)
	if err != nil {
		return err
	}
	if sc == nil {
		return fmt.Errorf("schedule %d not found", id)
	}
	return m.CancelPlannedSchedule(ctx, id, sc.Revision)
}

// StartNow starts a sell task straight away, even for a meal that hasn't
// begun, on autopilot or not, and returns its id with a zero time. While
// the meal before is still served it can't post yet, so it schedules the
// task for when that meal ends instead, replacing a schedule already
// waiting, and returns the schedule's id and that time.
func (m *sellManager) StartNow(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, autopilot bool) (int64, time.Time, error) {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return 0, time.Time{}, errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	slot, err := findSlot(m.cfg, date, meal)
	if err != nil {
		return 0, time.Time{}, err
	}
	if opens := listOpens(m.cfg, slot); time.Now().Before(opens) {
		if m.cfg.Live().HiddenMeal(meal) {
			return 0, time.Time{}, errors.New("snacks are turned off in your settings")
		}
		return m.schedule(ctx, userID, 0, 0, slot, date, meal, minRupees, maxRupees, autopilot, opens, true)
	}
	id, err := m.start(ctx, userID, date, meal, minRupees, maxRupees, "", autopilot)
	return id, time.Time{}, err
}

// SetScheduleAutopilot turns autopilot on or off for a schedule that
// hasn't started its task.
func (m *sellManager) SetScheduleAutopilot(ctx context.Context, id int64, on bool) error {
	ok, err := m.st.SetSellScheduleAutopilot(ctx, id, on)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("sell schedule %d is not waiting; it may have started already", id)
	}
	m.log.Info("sell schedule autopilot", "schedule", id, "on", on)
	m.changed()
	return nil
}

// resumeSchedules arms the timers of waiting schedules after a restart.
// One that came due while run was stopped starts now, if its meal hasn't
// ended. With sell tasks off they are all cancelled, as tasks are.
func (m *sellManager) resumeSchedules(ctx context.Context, enabled bool) error {
	all, err := m.st.SellSchedules(ctx, time.Now())
	if err != nil {
		return err
	}
	for _, sc := range all {
		if sc.State == store.ScheduleStarting || (sc.State == store.ScheduleStarted && sc.TaskID == 0) {
			if err := m.st.RecoverSellSchedule(ctx, sc.ID); err != nil {
				return err
			}
			fresh, err := m.st.GetSellSchedule(ctx, sc.ID)
			if err != nil {
				return err
			}
			sc = *fresh
		}
		if sc.State != store.ScheduleWaiting {
			continue
		}
		if !enabled {
			if _, err := m.st.EndSellSchedule(ctx, sc.ID, store.ScheduleCancelled, 0, "sell tasks are turned off in settings", time.Now()); err != nil {
				m.log.Error("cancel sell schedule", "schedule", sc.ID, "err", err)
			}
			continue
		}
		m.arm(sc)
	}
	return nil
}

func (m *sellManager) arm(sc store.SellSchedule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.base.Err() != nil {
		return
	}
	if old := m.timers[sc.ID]; old != nil {
		old.Stop()
	}
	// A schedule saved before the previous-meal limit may say earlier.
	at := sc.StartsAt
	if slot, err := findSlot(m.cfg, sc.Date, sc.Meal); err == nil {
		if opens := listOpens(m.cfg, slot); at.Before(opens) {
			at = opens
		}
	}
	if sc.RetryAt.After(at) {
		at = sc.RetryAt
	}
	m.timers[sc.ID] = time.AfterFunc(time.Until(at), func() { m.fire(sc.ID) })
}

func (m *sellManager) disarm(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.timers[id]; t != nil {
		t.Stop()
		delete(m.timers, id)
	}
}

// fire starts a schedule's sell task. Claiming the schedule first means a
// cancel arriving now either wins outright or finds it already started.
func (m *sellManager) fire(id int64) {
	m.mu.Lock()
	delete(m.timers, id)
	m.mu.Unlock()
	if m.base.Err() != nil {
		return // run is stopping; the next start resumes the schedule
	}
	ctx := context.WithoutCancel(m.base)
	sc, err := m.st.GetSellSchedule(ctx, id)
	if err != nil || sc == nil || sc.State != store.ScheduleWaiting {
		if err != nil {
			m.log.Error("read sell schedule", "schedule", id, "err", err)
		}
		return
	}
	if ok, err := m.st.ClaimSellSchedule(ctx, id, sc.Revision, time.Now()); err != nil || !ok {
		if err != nil {
			m.log.Error("claim sell schedule", "schedule", id, "err", err)
		} else if fresh, e := m.st.GetSellSchedule(ctx, id); e == nil && fresh != nil && fresh.State == store.ScheduleWaiting {
			m.arm(*fresh)
		}
		return
	}
	defer m.changed()
	fail := func(detail string) {
		m.log.Warn("scheduled sell task did not start", "schedule", id, "date", sc.Date, "meal", sc.Meal, "why", detail)
		if err := m.st.SetSellScheduleResult(ctx, id, store.ScheduleFailed, 0, detail); err != nil {
			m.log.Error("record sell schedule", "schedule", id, "err", err)
		}
	}
	switch {
	case !m.cfg.Live().MCP.SellTasks:
		fail("sell tasks are turned off in settings")
		return
	case !time.Now().Before(sc.EndsAt):
		fail("the meal ended before the offer went up")
		return
	}
	if m.connected != nil && !m.connected(2*time.Second) {
		m.retrySchedule(ctx, *sc, errors.New("WhatsApp is disconnected"))
		return
	}
	attemptCtx, cancel := context.WithTimeout(m.base, 30*time.Second)
	defer cancel()
	task, err := m.start(attemptCtx, sc.UserID, sc.Date, sc.Meal, int(sc.MinPaise/100), int(sc.MaxPaise/100), sc.Handoff, sc.Autopilot)
	if err != nil {
		// Starting manually can win while this attempt is checking the portal.
		if latest, e := m.st.GetSellSchedule(ctx, id); e == nil && latest != nil && latest.State == store.ScheduleStarted && latest.TaskID != 0 {
			return
		}
		if m.base.Err() != nil {
			_ = m.st.RecoverSellSchedule(ctx, id)
			return
		}
		var temporary interface{ Temporary() bool }
		var ne net.Error
		retry := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &temporary) && temporary.Temporary()) || (errors.As(err, &ne) && ne.Timeout())
		if retry {
			m.retrySchedule(ctx, *sc, err)
		} else {
			fail(err.Error())
		}
		return
	}
	if err := m.st.SetSellScheduleResult(ctx, id, store.ScheduleStarted, task, ""); err != nil {
		m.log.Error("record sell schedule", "schedule", id, "err", err)
	}
	m.log.Info("scheduled sell task started", "schedule", id, "task", task)
}

func (m *sellManager) changed() {
	if m.notify != nil {
		m.notify()
	}
}

// Connection and portal outages get five retries, capped by the meal's end.
func (m *sellManager) retrySchedule(ctx context.Context, sc store.SellSchedule, cause error) {
	delay := 15 * time.Second * time.Duration(1<<min(sc.Attempts, 4))
	at := time.Now().Add(delay)
	if sc.Attempts >= 5 || !at.Before(sc.EndsAt) {
		_ = m.st.SetSellScheduleResult(ctx, sc.ID, store.ScheduleFailed, 0, "Retries exhausted: "+cause.Error())
		return
	}
	if err := m.st.RetrySellSchedule(ctx, sc.ID, at, cause.Error()); err != nil {
		m.log.Error("save schedule retry", "err", err)
		return
	}
	sc.RetryAt = at
	sc.Attempts++
	m.arm(sc)
	m.changed()
}

func (m *sellManager) CancelPlannedSchedule(ctx context.Context, id, revision int64) error {
	ok, err := m.st.CancelPlannedSchedule(ctx, id, revision)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("schedule changed or started in another tab; refresh and review this meal")
	}
	m.disarm(id)
	m.changed()
	return nil
}
