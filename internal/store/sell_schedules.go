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
	ScheduleStarting  = "starting"
	ScheduleStarted   = "started"
	ScheduleCancelled = "cancelled"
	ScheduleFailed    = "failed"
)

type SellSchedule struct {
	RuleID, RuleRevision int64
	Overridden           bool
	ID                   int64
	Revision, Attempts   int64
	RetryAt              time.Time
	Date, Meal           string
	MinPaise, MaxPaise   int64
	UserID               int64
	Handoff              string
	StartsAt, EndsAt     time.Time
	State                string
	// TaskID is the task it started, once started.
	TaskID    int64
	Detail    string
	CreatedAt time.Time
	EndedAt   time.Time // zero while waiting
	// Autopilot starts the task on autopilot; see SellTask.Autopilot.
	Autopilot bool
	// Extra is the extra item name when selling an extra separately (e.g. "Egg").
	Extra string
}

const sellScheduleCols = `id,date,meal,min_paise,max_paise,user_id,handoff,starts_at,ends_at,state,task_id,detail,created_at,ended_at,autopilot,revision,attempts,retry_at,rule_id,rule_revision,overridden,extra`

func scanSellSchedule(row interface{ Scan(...any) error }) (SellSchedule, error) {
	var s SellSchedule
	var starts, ends, created, ended, retry int64
	err := row.Scan(&s.ID, &s.Date, &s.Meal, &s.MinPaise, &s.MaxPaise, &s.UserID, &s.Handoff, &starts, &ends, &s.State, &s.TaskID, &s.Detail, &created, &ended, &s.Autopilot, &s.Revision, &s.Attempts, &retry, &s.RuleID, &s.RuleRevision, &s.Overridden, &s.Extra)
	s.StartsAt, s.EndsAt, s.CreatedAt = time.Unix(starts, 0), time.Unix(ends, 0), time.Unix(created, 0)
	if retry != 0 {
		s.RetryAt = time.Unix(retry, 0)
	}
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
	if sc.RuleID > 0 {
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recurring_rules WHERE id=? AND user_id=? AND revision=? AND enabled=1 AND deleted=0) AND NOT EXISTS(SELECT 1 FROM planner_kept WHERE user_id=? AND date=? AND meal=?) AND NOT EXISTS(SELECT 1 FROM sell_schedules WHERE rule_id>0 AND date=? AND meal=? AND state!='cancelled')`, sc.RuleID, sc.UserID, sc.RuleRevision, sc.UserID, sc.Date, sc.Meal, sc.Date, sc.Meal).Scan(&valid); err != nil {
			return 0, err
		}
		if !valid {
			return 0, errors.New("rule changed, meal is kept, or this occurrence was already generated")
		}
	}

	for _, c := range []struct{ q, msg string }{
		{`SELECT EXISTS(SELECT 1 FROM qr_deliveries WHERE date=? AND meal=?)`, "meal already has a delivery record; check the sales page"},
		{`SELECT EXISTS(SELECT 1 FROM sell_tasks WHERE date=? AND meal=? AND state='active')`, "a sell task for this meal is already running"},
		{`SELECT EXISTS(SELECT 1 FROM sell_schedules WHERE date=? AND meal=? AND state IN ('waiting','starting'))`, "this meal is already scheduled to sell"},
	} {
		var exists bool
		if err := tx.QueryRowContext(ctx, c.q, sc.Date, sc.Meal).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, errors.New(c.msg)
		}
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO sell_schedules(date,meal,min_paise,max_paise,user_id,handoff,starts_at,ends_at,created_at,autopilot,rule_id,rule_revision,extra)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, sc.Date, sc.Meal, sc.MinPaise, sc.MaxPaise, sc.UserID, sc.Handoff, sc.StartsAt.Unix(), sc.EndsAt.Unix(), sc.CreatedAt.Unix(), sc.Autopilot, sc.RuleID, sc.RuleRevision, sc.Extra)
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	if sc.RuleID == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM planner_kept WHERE user_id=? AND date=? AND meal=?`, sc.UserID, sc.Date, sc.Meal); err != nil {
			return 0, err
		}
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+sellScheduleCols+` FROM sell_schedules WHERE state IN ('waiting','starting') OR (state='started' AND task_id=0) OR created_at>=? OR ended_at>=? ORDER BY id DESC`, since.Unix(), since.Unix())
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
	err := s.db.QueryRowContext(ctx, `UPDATE sell_schedules SET state=?,task_id=?,detail=?,ended_at=?,revision=revision+1 WHERE date=? AND meal=? AND state='waiting' RETURNING id`,
		state, taskID, detail, at.Unix(), date, meal).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SetSellScheduleAutopilot turns autopilot on or off for a waiting
// schedule. It reports false when the schedule isn't waiting.
func (s *Store) SetSellScheduleAutopilot(ctx context.Context, id int64, on bool) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET autopilot=?,overridden=CASE WHEN rule_id>0 THEN 1 ELSE 0 END,revision=revision+1 WHERE id=? AND state='waiting'`, on, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// UpdateSellSchedule edits a waiting schedule atomically; a stale browser cannot
// overwrite newer prices or a timer that has already claimed the meal.
func (s *Store) UpdateSellSchedule(ctx context.Context, sc SellSchedule) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET min_paise=?,max_paise=?,starts_at=?,autopilot=?,overridden=CASE WHEN rule_id>0 THEN 1 ELSE 0 END,revision=revision+1,detail='',retry_at=0,attempts=0 WHERE id=? AND user_id=? AND state='waiting' AND revision=?`, sc.MinPaise, sc.MaxPaise, sc.StartsAt.Unix(), sc.Autopilot, sc.ID, sc.UserID, sc.Revision)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s *Store) RetrySellSchedule(ctx context.Context, id int64, at time.Time, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET state='waiting',retry_at=?,attempts=attempts+1,detail=?,ended_at=0,revision=revision+1 WHERE id=? AND state='starting'`, at.Unix(), detail, id)
	return err
}

// RecoverSellSchedule reconciles a crash on either side of task creation.
// CreateSellTask links the task in its own transaction, before any messages.
func (s *Store) RecoverSellSchedule(ctx context.Context, id int64) error {
	// Older versions could create a task without recording its ID.
	if _, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET task_id=coalesce((SELECT id FROM sell_tasks WHERE date=sell_schedules.date AND meal=sell_schedules.meal AND created_at>=sell_schedules.ended_at ORDER BY id DESC LIMIT 1),0) WHERE id=? AND task_id=0 AND state='started'`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET state=CASE WHEN task_id>0 THEN 'started' ELSE 'waiting' END,detail=CASE WHEN task_id>0 THEN detail ELSE 'Resuming interrupted start' END,revision=revision+1 WHERE id=? AND (state='starting' OR (state='started' AND task_id=0))`, id)
	return err
}

func (s *Store) CancelPlannedSchedule(ctx context.Context, id, revision int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	sc, err := scanSellSchedule(tx.QueryRowContext(ctx, `SELECT `+sellScheduleCols+` FROM sell_schedules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE sell_schedules SET state='cancelled',detail='Kept this meal',ended_at=?,revision=revision+1 WHERE id=? AND state='waiting' AND revision=?`, time.Now().Unix(), id, revision)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if err := keepSchedule(ctx, tx, sc); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ClaimSellSchedule checks the same revision and posting time that the timer
// read. Editing a timer concurrently cannot start it at the old time.
func (s *Store) ClaimSellSchedule(ctx context.Context, id, revision int64, now time.Time) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE sell_schedules SET state='starting',ended_at=? WHERE id=? AND revision=? AND state='waiting' AND starts_at<=? AND retry_at<=?`, now.Unix(), id, revision, now.Unix(), now.Unix())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
