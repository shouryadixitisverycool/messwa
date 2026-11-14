package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// A sell schedule is a sell task waiting for its meal: it starts the task
// at StartsAt, a set time around the meal's start. Nothing is posted or
// sent before then, so a waiting schedule holds no sell task row and
// never joins a pool.
const sellScheduleSchema = `
CREATE TABLE IF NOT EXISTS sell_schedules (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 date TEXT NOT NULL,
 meal TEXT NOT NULL,
 min_paise INTEGER NOT NULL,
 max_paise INTEGER NOT NULL,
 user_id INTEGER NOT NULL,
 handoff TEXT NOT NULL DEFAULT '',
 starts_at INTEGER NOT NULL,
 ends_at INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'waiting',
 task_id INTEGER NOT NULL DEFAULT 0,
 detail TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 ended_at INTEGER NOT NULL DEFAULT 0,
 autopilot INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS sell_schedules_one_waiting ON sell_schedules(date, meal) WHERE state='waiting';
`

// Sell schedule states. Only a waiting schedule starts a task.
const (
	ScheduleWaiting   = "waiting"
	ScheduleStarted   = "started"
	ScheduleCancelled = "cancelled"
	ScheduleFailed    = "failed"
)

type SellSchedule struct {
	ID                 int64
	Date, Meal         string
	MinPaise, MaxPaise int64
	UserID             int64
	Handoff            string
	StartsAt, EndsAt   time.Time
	State              string
	// TaskID is the task it started, once started.
	TaskID    int64
	Detail    string
	CreatedAt time.Time
	EndedAt   time.Time // zero while waiting
	// Autopilot starts the task on autopilot; see SellTask.Autopilot.
	Autopilot bool
}

const sellScheduleCols = `id,date,meal,min_paise,max_paise,user_id,handoff,starts_at,ends_at,state,task_id,detail,created_at,ended_at,autopilot`

func scanSellSchedule(row interface{ Scan(...any) error }) (SellSchedule, error) {
	var s SellSchedule
	var starts, ends, created, ended int64
	err := row.Scan(&s.ID, &s.Date, &s.Meal, &s.MinPaise, &s.MaxPaise, &s.UserID, &s.Handoff, &starts, &ends, &s.State, &s.TaskID, &s.Detail, &created, &ended, &s.Autopilot)
	s.StartsAt, s.EndsAt, s.CreatedAt = time.Unix(starts, 0), time.Unix(ends, 0), time.Unix(created, 0)
	if ended != 0 {
		s.EndedAt = time.Unix(ended, 0)
	}
	return s, err
}

// CreateSellSchedule saves a waiting schedule. It fails when the meal
// already has a waiting schedule, a running task or a delivery record.
func (s *Store) CreateSellSchedule(ctx context.Context, sc SellSchedule) (int64, error) {
	if sc.MinPaise <= 0 || sc.MaxPaise < sc.MinPaise {
		return 0, errors.New("prices must be positive with max at least min")
	}
	if !sc.StartsAt.Before(sc.EndsAt) {
		return 0, errors.New("the schedule starts after the meal ends")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, c := range []struct{ q, msg string }{
		{`SELECT EXISTS(SELECT 1 FROM qr_deliveries WHERE date=? AND meal=?)`, "meal already has a delivery record; check the sales page"},
		{`SELECT EXISTS(SELECT 1 FROM sell_tasks WHERE date=? AND meal=? AND state='active')`, "a sell task for this meal is already running"},
		{`SELECT EXISTS(SELECT 1 FROM sell_schedules WHERE date=? AND meal=? AND state='waiting')`, "this meal is already scheduled to sell"},
	} {
		var exists bool
		if err := tx.QueryRowContext(ctx, c.q, sc.Date, sc.Meal).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, errors.New(c.msg)
		}
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO sell_schedules(date,meal,min_paise,max_paise,user_id,handoff,starts_at,ends_at,created_at,autopilot)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, sc.Date, sc.Meal, sc.MinPaise, sc.MaxPaise, sc.UserID, sc.Handoff, sc.StartsAt.Unix(), sc.EndsAt.Unix(), sc.CreatedAt.Unix(), sc.Autopilot)
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// GetSellSchedule returns nil when there is no such schedule.
func (s *Store) GetSellSchedule(ctx context.Context, id int64) (*SellSchedule, error) {
	sc, err := scanSellSchedule(s.db.QueryRowContext(ctx, `SELECT `+sellScheduleCols+` FROM sell_schedules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

// SellSchedules returns waiting schedules and those created since since,
// newest first.
func (s *Store) SellSchedules(ctx context.Context, since time.Time) ([]SellSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sellScheduleCols+` FROM sell_schedules WHERE state='waiting' OR created_at>=? ORDER BY id DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellSchedule
	for rows.Next() {
		sc, err := scanSellSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// EndSellSchedule moves a waiting schedule to state, with the task it
// started (0 for none). It reports false when the schedule wasn't waiting,
// so a cancel and the start racing it can't both win.
func (s *Store) EndSellSchedule(ctx context.Context, id int64, state string, taskID int64, detail string, at time.Time) (bool, error) {
	if state == ScheduleWaiting {
		return false, errors.New("cannot end a schedule as waiting")
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET state=?,task_id=?,detail=?,ended_at=? WHERE id=? AND state='waiting'`, state, taskID, detail, at.Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// SetSellScheduleResult records what came of a schedule that already left
// waiting: the task it started, or why it couldn't start one.
func (s *Store) SetSellScheduleResult(ctx context.Context, id int64, state string, taskID int64, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET state=?,task_id=?,detail=? WHERE id=? AND state!='waiting'`, state, taskID, detail, id)
	return err
}

// EndWaitingSellSchedule ends the meal's waiting schedule, if it has one,
// and returns its id (0 for none).
func (s *Store) EndWaitingSellSchedule(ctx context.Context, date, meal, state string, taskID int64, detail string, at time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `UPDATE sell_schedules SET state=?,task_id=?,detail=?,ended_at=? WHERE date=? AND meal=? AND state='waiting' RETURNING id`,
		state, taskID, detail, at.Unix(), date, meal).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SetSellScheduleAutopilot turns autopilot on or off for a waiting
// schedule. It reports false when the schedule isn't waiting.
func (s *Store) SetSellScheduleAutopilot(ctx context.Context, id int64, on bool) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET autopilot=? WHERE id=? AND state='waiting'`, on, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
