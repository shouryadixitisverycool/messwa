package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"messwa/internal/store"
)

// Scheduled sales. Selling a meal that hasn't started yet doesn't post
// anything straight away: it saves a schedule, and at [sell] list_at
// around the meal's start a timer starts the sell task as if you had
// clicked then. The task runs till the meal ends, like any other.
//
// Everything a click would check (the registration, the groups, a sale
// already running) is checked when you schedule, so a mistake shows then,
// and again when the task starts. A waiting schedule has no sell task row,
// so it never joins a pool or changes a pool's prices.

// StartOrSchedule starts a sell task for the meal now when it is being
// served or its list_at time has passed. Otherwise it schedules one and
// returns the schedule's id and when the task will start.
func (m *sellManager) StartOrSchedule(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, autopilot bool) (int64, time.Time, error) {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return 0, time.Time{}, errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	slot, err := findSlot(m.cfg, date, meal)
	if err != nil {
		return 0, time.Time{}, err
	}
	now := time.Now()
	at := listTime(slot.Start, slot.End, m.cfg.Live().Sell.ListAt.Duration)
	if !now.Before(slot.Start) || !now.Before(at) {
		id, err := m.start(ctx, userID, date, meal, minRupees, maxRupees, "", autopilot)
		return id, time.Time{}, err
	}
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
	sc := store.SellSchedule{Date: date, Meal: meal, MinPaise: int64(minRupees) * 100, MaxPaise: int64(maxRupees) * 100,
		UserID: userID, StartsAt: at, EndsAt: slot.End, CreatedAt: now, Autopilot: autopilot}
	if sc.ID, err = m.st.CreateSellSchedule(ctx, sc); err != nil {
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
// at or after the end.
func listTime(start, end time.Time, offset time.Duration) time.Time {
	at := start.Add(offset)
	if !at.Before(end) {
		return start
	}
	return at
}

// CancelSchedule drops a waiting schedule before its task starts.
func (m *sellManager) CancelSchedule(ctx context.Context, id int64) error {
	ok, err := m.st.EndSellSchedule(ctx, id, store.ScheduleCancelled, 0, "cancelled", time.Now())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("sell schedule %d is not waiting; it may have started already", id)
	}
	m.disarm(id)
	m.log.Info("sell schedule cancelled", "schedule", id)
	m.changed()
	return nil
}

// StartNow starts a sell task straight away, even for a meal that hasn't
// begun, on autopilot or not.
func (m *sellManager) StartNow(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, autopilot bool) (int64, error) {
	return m.start(ctx, userID, date, meal, minRupees, maxRupees, "", autopilot)
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
	m.timers[sc.ID] = time.AfterFunc(time.Until(sc.StartsAt), func() { m.fire(sc.ID) })
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
	if ok, err := m.st.EndSellSchedule(ctx, id, store.ScheduleStarted, 0, "", time.Now()); err != nil || !ok {
		if err != nil {
			m.log.Error("claim sell schedule", "schedule", id, "err", err)
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
	task, err := m.start(m.base, sc.UserID, sc.Date, sc.Meal, int(sc.MinPaise/100), int(sc.MaxPaise/100), sc.Handoff, sc.Autopilot)
	if err != nil {
		fail(err.Error())
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
