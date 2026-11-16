package main

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"messwa/internal/config"
	"messwa/internal/store"
)

// activeTask is the meal's active sell task, or nil.
func (f *sellFixture) activeTask(meal string) *store.SellTask {
	f.t.Helper()
	tasks, err := f.st.SellTasks(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		f.t.Fatal(err)
	}
	for _, t := range tasks {
		if t.Meal == meal && t.State == store.TaskActive {
			return &t
		}
	}
	return nil
}

func (f *sellFixture) schedule(id int64) store.SellSchedule {
	f.t.Helper()
	sc, err := f.st.GetSellSchedule(context.Background(), id)
	if err != nil || sc == nil {
		f.t.Fatalf("schedule %d: %v", id, err)
	}
	return *sc
}

func TestSellScheduleListsBeforeTheMeal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		slot, err := findSlot(f.cfg, f.date, "lunch")
		if err != nil {
			t.Fatal(err)
		}
		id, at, err := m.StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		if want := slot.Start.Add(-15 * time.Minute); !at.Equal(want) {
			t.Fatalf("scheduled for %s, want %s", at, want)
		}
		if _, _, err := m.StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false); err == nil {
			t.Fatal("scheduled the same meal twice")
		}

		time.Sleep(time.Until(at) - time.Minute)
		synctest.Wait()
		if f.activeTask("lunch") != nil || len(f.conn.messages()) != 0 {
			t.Fatal("the task started before its time")
		}

		time.Sleep(2 * time.Minute)
		synctest.Wait()
		task := f.activeTask("lunch")
		if task == nil {
			t.Fatalf("no task once the schedule was due: %+v", f.schedule(id))
		}
		if task.MinPaise != 4000 || task.MaxPaise != 6000 || !task.EndsAt.Equal(slot.End) {
			t.Fatalf("task %+v", task)
		}
		if sc := f.schedule(id); sc.State != store.ScheduleStarted || sc.TaskID != task.ID {
			t.Fatalf("schedule %+v, want started with task %d", sc, task.ID)
		}
		if len(sentTo(f.conn.messages(), f.group)) == 0 {
			t.Fatal("the scheduled task posted no offer")
		}
		cancel()
		synctest.Wait()
	})
}

func TestSellScheduleCancelAndStartEarly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)

		id, at, err := m.StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.CancelSchedule(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := m.CancelSchedule(ctx, id); err == nil {
			t.Fatal("cancelled a schedule twice")
		}

		// Starting by hand replaces a waiting schedule.
		id, _, err = m.StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		task, err := m.Start(ctx, 0, f.date, "lunch", 45, 70, "")
		if err != nil {
			t.Fatal(err)
		}
		if sc := f.schedule(id); sc.State != store.ScheduleStarted || sc.TaskID != task {
			t.Fatalf("schedule %+v after starting by hand", sc)
		}
		time.Sleep(time.Until(at) + time.Minute)
		synctest.Wait()
		if got := f.activeTask("lunch"); got == nil || got.ID != task {
			t.Fatalf("active task %+v, want the one started by hand", got)
		}
		cancel()
		synctest.Wait()
	})
}

func TestSellScheduleResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, stop := context.WithCancel(t.Context())
		id, at, err := f.manager(ctx).StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		stop()
		synctest.Wait()
		// run was down when it came due.
		time.Sleep(time.Until(at) + 5*time.Minute)
		synctest.Wait()
		if f.activeTask("lunch") != nil {
			t.Fatal("a stopped run started the task")
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(ctx).Resume(ctx, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if f.activeTask("lunch") == nil {
			t.Fatalf("no task after resuming a due schedule: %+v", f.schedule(id))
		}
		cancel()
		synctest.Wait()
	})
}

func TestSellScheduleStartsNowOnceDue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		slot, _ := findSlot(f.cfg, f.date, "lunch")
		time.Sleep(time.Until(slot.Start.Add(-10 * time.Minute)))
		id, at, err := m.StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		if !at.IsZero() || f.activeTask("lunch") == nil || f.activeTask("lunch").ID != id {
			t.Fatalf("past list_at it should start now: id %d at %s", id, at)
		}
		cancel()
		synctest.Wait()
	})
}

func TestSellScheduleFailsWhenSellTasksTurnOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		id, at, err := f.manager(ctx).StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		// Turned off the way the settings page does it: an atomic swap of
		// the live config, which the schedule's timer reads.
		if _, err := f.cfg.Update(config.Settings{"mcp.sell_tasks": []byte("false")}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(at) + time.Minute)
		synctest.Wait()
		if sc := f.schedule(id); sc.State != store.ScheduleFailed || f.activeTask("lunch") != nil {
			t.Fatalf("schedule %+v with sell tasks off", sc)
		}
		cancel()
		synctest.Wait()
	})
}

func TestListTime(t *testing.T) {
	start := time.Date(2026, 11, 11, 12, 30, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	for _, c := range []struct {
		offset time.Duration
		want   time.Time
	}{
		{-15 * time.Minute, start.Add(-15 * time.Minute)},
		{0, start},
		{90 * time.Minute, start.Add(90 * time.Minute)},
		// Past the end it lists at the start.
		{2 * time.Hour, start},
	} {
		if got := listTime(start, end, c.offset, time.Time{}); !got.Equal(c.want) {
			t.Errorf("listTime(%s) = %s, want %s", c.offset, got, c.want)
		}
	}
	// Never before the meal before it ends.
	opens := start.Add(-5 * time.Minute)
	if got := listTime(start, end, -15*time.Minute, opens); !got.Equal(opens) {
		t.Errorf("listTime with the meal before ending at %s = %s", opens, got)
	}
	if got := listTime(start, end, 30*time.Minute, opens); !got.Equal(start.Add(30 * time.Minute)) {
		t.Errorf("listTime(30m) moved for a meal before that had ended: %s", got)
	}
}

// TestSellWaitsForTheMealBefore covers the rule that no offer goes up while
// the meal before it is still served: a schedule waits for that meal to
// end even when list_at says earlier, and Start now schedules the task for
// then, replacing the waiting schedule.
func TestSellWaitsForTheMealBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		// Snacks runs till 19:20, ten minutes before dinner, so list_at's
		// 19:15 would land inside it.
		for i := range f.cfg.Meals {
			if f.cfg.Meals[i].Name == "snacks" {
				f.cfg.Meals[i].Ends = "19:20"
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		snacks, err := findSlot(f.cfg, f.date, "snacks")
		if err != nil {
			t.Fatal(err)
		}
		old, at, err := m.StartOrSchedule(ctx, 0, f.date, "dinner", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		if !at.Equal(snacks.End) {
			t.Fatalf("scheduled for %s, want %s when snacks ends", at, snacks.End)
		}
		id, at, err := m.StartNow(ctx, 0, f.date, "dinner", 45, 70, true)
		if err != nil {
			t.Fatal(err)
		}
		if id == old || !at.Equal(snacks.End) {
			t.Fatalf("StartNow during snacks = schedule %d at %s, want a new one at %s", id, at, snacks.End)
		}
		if sc := f.schedule(old); sc.State != store.ScheduleCancelled {
			t.Fatalf("replaced schedule %+v, want cancelled", sc)
		}
		if f.activeTask("dinner") != nil {
			t.Fatal("StartNow posted dinner during snacks")
		}
		time.Sleep(time.Until(at) - time.Minute)
		synctest.Wait()
		if f.activeTask("dinner") != nil {
			t.Fatal("the dinner task started during snacks")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		task := f.activeTask("dinner")
		if task == nil {
			t.Fatalf("no dinner task once snacks ended: %+v", f.schedule(id))
		}
		if task.MinPaise != 4500 || task.MaxPaise != 7000 || !task.Autopilot {
			t.Fatalf("task %+v, want StartNow's prices on autopilot", task)
		}
	})
}

// TestOldScheduleWaitsForTheMealBefore covers a schedule saved for a time
// inside the meal before, as ones from before the rule can be: its timer
// waits for that meal to end.
func TestOldScheduleWaitsForTheMealBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		dinner, err := findSlot(f.cfg, f.date, "dinner")
		if err != nil {
			t.Fatal(err)
		}
		opens := listOpens(f.cfg, dinner)
		sc := store.SellSchedule{Date: f.date, Meal: "dinner", MinPaise: 4000, MaxPaise: 6000,
			StartsAt: opens.Add(-time.Hour), EndsAt: dinner.End, CreatedAt: time.Now()}
		if sc.ID, err = f.st.CreateSellSchedule(ctx, sc); err != nil {
			t.Fatal(err)
		}
		sc.State = store.ScheduleWaiting
		m := f.manager(ctx)
		m.arm(sc)
		time.Sleep(time.Until(opens) - time.Minute)
		synctest.Wait()
		if f.activeTask("dinner") != nil {
			t.Fatal("an old schedule started during snacks")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if f.activeTask("dinner") == nil {
			t.Fatalf("no dinner task once snacks ended: %+v", f.schedule(sc.ID))
		}
	})
}

func TestSellScheduleStartsOnAutopilot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.MCP.SellTasks = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, at, err := m.StartOrSchedule(ctx, 0, f.date, "lunch", 40, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		if f.schedule(id).Autopilot {
			t.Fatal("autopilot on by default")
		}
		if err := m.SetScheduleAutopilot(ctx, id, true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(at) + time.Minute)
		synctest.Wait()
		task := f.activeTask("lunch")
		if task == nil || !task.Autopilot {
			t.Fatalf("task %+v, want it on autopilot", task)
		}
		if err := m.SetScheduleAutopilot(ctx, id, false); err == nil {
			t.Fatal("changed autopilot on a schedule that already started")
		}
		cancel()
		synctest.Wait()
	})
}

// TestHiddenMealDoesNotHoldTheNextOffer covers hide_snacks: dinner's offer
// waits for lunch, not snacks.
func TestHiddenMealDoesNotHoldTheNextOffer(t *testing.T) {
	f := newSellFixture(t, filepath.Join(t.TempDir(), "data.db"))
	dinner, err := findSlot(f.cfg, f.date, "dinner")
	if err != nil {
		t.Fatal(err)
	}
	if prev, _ := prevMeal(f.cfg, dinner); prev.Meal != "snacks" {
		t.Fatalf("meal before dinner %q, want snacks", prev.Meal)
	}
	f.cfg.HideSnacks = true
	prev, _ := prevMeal(f.cfg, dinner)
	if prev.Meal != "lunch" {
		t.Fatalf("meal before dinner with snacks hidden %q, want lunch", prev.Meal)
	}
	if got := listOpens(f.cfg, dinner); !got.Equal(prev.End) {
		t.Fatalf("dinner opens %v, want lunch's end %v", got, prev.End)
	}
}
