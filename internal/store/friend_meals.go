package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A friend's meal is one you sell for someone else, from their QR image,
// with the mess and diet stated by hand. Each gets its own slot name, like
// "dinner#f1", so it never collides with your own dinner: sell tasks and
// QR deliveries are keyed by date and slot, and buyers still see "dinner".
const friendMealSchema = `
CREATE TABLE IF NOT EXISTS friend_meals (
 date TEXT NOT NULL,
 slot TEXT NOT NULL,
 mess TEXT NOT NULL,
 friend TEXT NOT NULL DEFAULT '',
 extras TEXT NOT NULL DEFAULT '',
 qr BLOB NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(date, slot)
);`

// FriendMeal is a friend's meal. Mess is portal style, e.g.
// "kadamba-nonveg". QR is left empty by FriendMeals.
type FriendMeal struct {
	Date, Slot string
	Mess       string
	Friend     string
	Extras     []string
	QR         []byte
	CreatedAt  time.Time
}

// FriendSlotPrefix marks a friend's meal slot: "dinner" + "#f" + n.
const FriendSlotPrefix = "#f"

// IsFriendSlot reports whether a meal name is a friend's meal slot.
func IsFriendSlot(meal string) bool { return strings.Contains(meal, FriendSlotPrefix) }

// AddFriendMeal saves m under the first free slot for its meal, which it
// takes from m.Slot ("dinner"), and returns that slot.
func (s *Store) AddFriendMeal(ctx context.Context, m FriendMeal) (string, error) {
	if m.Date == "" || m.Slot == "" || strings.Contains(m.Slot, "#") || m.Mess == "" || len(m.QR) == 0 {
		return "", errors.New("a friend's meal needs a date, meal, mess and QR")
	}
	for n := 1; n <= 99; n++ {
		slot := fmt.Sprintf("%s%s%d", m.Slot, FriendSlotPrefix, n)
		r, err := s.db.ExecContext(ctx, `INSERT INTO friend_meals(date,slot,mess,friend,extras,qr,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			m.Date, slot, m.Mess, m.Friend, strings.Join(m.Extras, "\n"), m.QR, m.CreatedAt.Unix())
		if err != nil {
			return "", err
		}
		if n, _ := r.RowsAffected(); n == 1 {
			return slot, nil
		}
	}
	return "", errors.New("too many friends' meals for this meal")
}

// FriendMeal returns the friend's meal in that slot, with its QR, or nil.
func (s *Store) FriendMeal(ctx context.Context, date, slot string) (*FriendMeal, error) {
	m := FriendMeal{Date: date, Slot: slot}
	var extras string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT mess,friend,extras,qr,created_at FROM friend_meals WHERE date=? AND slot=?`, date, slot).
		Scan(&m.Mess, &m.Friend, &extras, &m.QR, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Extras = splitExtras(extras)
	m.CreatedAt = time.Unix(at, 0)
	return &m, nil
}

// DeleteFriendMeal forgets a friend's meal, after its task failed to start.
func (s *Store) DeleteFriendMeal(ctx context.Context, date, slot string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM friend_meals WHERE date=? AND slot=?`, date, slot)
	return err
}

// FriendMeals lists friends' meals on or after the date from, oldest
// first, without their QR images.
func (s *Store) FriendMeals(ctx context.Context, from string) ([]FriendMeal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT date,slot,mess,friend,extras,created_at FROM friend_meals WHERE date>=? ORDER BY date, created_at, slot`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FriendMeal
	for rows.Next() {
		var m FriendMeal
		var extras string
		var at int64
		if err := rows.Scan(&m.Date, &m.Slot, &m.Mess, &m.Friend, &extras, &at); err != nil {
			return nil, err
		}
		m.Extras = splitExtras(extras)
		m.CreatedAt = time.Unix(at, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

func splitExtras(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
