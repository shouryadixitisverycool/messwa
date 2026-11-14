package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Pools. Sell tasks on one account that sell the same meal (date, mess,
// diet and meal, whatever slot suffix like "dinner#f1" each carries) and
// that negotiate without handing anything off share one group post and
// one stream of buyers. The bot haggles down to the lowest floor among
// them, and a buyer who agrees goes to the oldest task whose own floor
// takes the price. A task that is fixed-price or hands off sells alone.

// BuyerMoved is a buyer the task passed to another task in its pool.
const BuyerMoved = "moved"

// EventPooled is a buyer passed between two tasks in a pool. Peer: the
// buyer; N: the agreed price in paise, if any; Text: "to N" on the task
// they left, "from N" on the one they joined.
const EventPooled = "pooled"

// EventEdited is a copy of the offer edited to show the pool's count or
// price now. Peer: the group; Text: the new text.
const EventEdited = "edited"

// pooledCond holds for a task alias x that can pool.
func pooledCond(x string) string {
	return x + `.max_paise>` + x + `.min_paise AND ` + x + `.handoff IN ('','never') AND ` + x + `.mess!='' AND ` + x + `.autopilot=0`
}

// baseMealOf is the configured meal behind alias x's slot name.
func baseMealOf(x string) string {
	return `substr(` + x + `.meal,1,CASE WHEN instr(` + x + `.meal,'#')>0 THEN instr(` + x + `.meal,'#')-1 ELSE length(` + x + `.meal) END)`
}

// samePool holds when task p is in task t's pool. A task that can't pool
// is in a pool of its own.
var samePool = `(p.id=t.id OR (` + pooledCond("t") + ` AND ` + pooledCond("p") + ` AND p.date=t.date AND p.mess=t.mess
 AND p.diet=t.diet AND ` + baseMealOf("p") + `=` + baseMealOf("t") + `))`

// poolFloor is the lowest floor among the active tasks in task ?'s pool,
// or the task's own floor once it has ended.
var poolFloor = `(SELECT min(p.min_paise) FROM sell_tasks t JOIN sell_tasks p ON ` + samePool + `
 AND (p.id=t.id OR p.state='active') WHERE t.id=?)`

// poolHonoured is honoured with the pool's floor: a price the bot may
// quote. Agreeing, arming and charging still check the task's own floor.
var poolHonoured = strings.Replace(honoured, `(SELECT min_paise FROM sell_tasks WHERE id=?)`, poolFloor, 1)

// SellTaskPool returns every task in id's pool on its date, whatever its
// state, oldest first. A task that can't pool is alone in it.
func (s *Store) SellTaskPool(ctx context.Context, id int64) ([]SellTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+strings.ReplaceAll(sellTaskColsT, "t.", "p.")+`
 FROM sell_tasks t JOIN sell_tasks p ON `+samePool+` WHERE t.id=? ORDER BY p.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellTask
	for rows.Next() {
		t, err := scanSellTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PassSellTaskBuyer moves a buyer still in play (queued, negotiating,
// agreed, or timed out and so revivable) from task from to task to in the same pool, haggle and all. The
// row left behind becomes moved. With agreed set, the buyer arrives having
// agreed that price, with n as their haggle; the price must meet to's own
// floor. A buyer who arrives agreed under an earlier deal must meet it
// too. Prices quoted under to's floor are dropped from the moved row, so
// to never charges less than its floor. It reports false when the buyer
// isn't in play, to isn't active, or the price is under to's floor.
func (s *Store) PassSellTaskBuyer(ctx context.Context, from, to int64, peer, agreed string, n Negotiation, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var toMin int64
	var toState string
	switch err := tx.QueryRowContext(ctx, `SELECT t.min_paise,t.state FROM sell_tasks t JOIN sell_tasks p ON `+samePool+`
 WHERE t.id=? AND p.id=?`, to, from).Scan(&toMin, &toState); {
	case errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("sell tasks %d and %d are not in one pool", from, to)
	case err != nil:
		return false, err
	case toState != TaskActive:
		return false, nil
	}
	var state, had, quoted string
	if err := tx.QueryRowContext(ctx, `SELECT state,agreed,quoted FROM sell_task_buyers WHERE task_id=? AND peer=?`, from, peer).Scan(&state, &had, &quoted); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if state != BuyerQueued && state != BuyerNegotiating && state != BuyerAgreed && state != BuyerTimedOut {
		return false, nil
	}
	price := agreed
	if price == "" && state == BuyerAgreed {
		price = had
	}
	if price != "" {
		paise, err := ParsePaise(price)
		if err != nil {
			return false, err
		}
		if paise < toMin {
			return false, nil
		}
	}
	cols, err := buyerColumns(ctx, tx)
	if err != nil {
		return false, err
	}
	list := strings.Join(cols, ",")
	if _, err := tx.ExecContext(ctx, `DELETE FROM sell_task_buyers WHERE task_id=? AND peer=?`, to, peer); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sell_task_buyers(task_id,`+list+`) SELECT ?,`+list+`
 FROM sell_task_buyers WHERE task_id=? AND peer=?`, to, from, peer); err != nil {
		return false, err
	}
	var kept []int64
	for _, q := range splitPaise(quoted) {
		if q >= toMin {
			kept = append(kept, q)
		}
	}
	if agreed != "" {
		kept = nil
		for _, q := range n.Quoted {
			if q >= toMin {
				kept = append(kept, q)
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE sell_task_buyers SET state='agreed',agreed=?,agreed_at=?,ask=?,rounds=?,bot_msgs=?,unknowns=?,last_offer=?,
 quoted=?,said_lowest=?,pass_countered=?,nudges=? WHERE task_id=? AND peer=?`,
			agreed, at.UnixNano(), n.Ask, n.Rounds, n.BotMsgs, n.Unknowns, n.LastOffer, joinPaise(dedupe(kept)), n.SaidLowest, n.PassCountered, n.Nudges,
			to, peer)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE sell_task_buyers SET quoted=? WHERE task_id=? AND peer=?`, joinPaise(dedupe(kept)), to, peer)
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sell_task_buyers SET state=?,note=? WHERE task_id=? AND peer=?`,
		BuyerMoved, fmt.Sprintf("passed to sell task %d in the same pool", to), from, peer); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// buyerColumns are sell_task_buyers' columns besides task_id.
func buyerColumns(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('sell_task_buyers') ORDER BY cid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c != "task_id" {
			cols = append(cols, c)
		}
	}
	return cols, rows.Err()
}

func dedupe(xs []int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// SetOfferText records the text a copy of the offer shows, after it was
// posted or edited.
func (s *Store) SetOfferText(ctx context.Context, id int64, offerID, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_offers SET text=? WHERE task_id=? AND offer_id=?`, text, id, offerID)
	return err
}
