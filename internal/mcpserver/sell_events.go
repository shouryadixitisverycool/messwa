package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/store"
)

// sell_task_events returns the sell_task_events table for a task or a time
// window, so a test run can be reviewed from messwa itself instead of a
// script that tails the container log. Like the table, it holds what the
// bot read into buyer messages, never what they wrote.

const (
	sellEventsWindow = 2 * time.Hour
	sellEventsLimit  = 500
	sellEventsMax    = 2000
)

type SellEventsIn struct {
	ID    int64  `json:"id,omitempty" jsonschema:"sell task id; omit for every task's events in the window"`
	Since string `json:"since,omitempty" jsonschema:"start of the window, inclusive: 'YYYY-MM-DD HH:MM[:SS]' or 'HH:MM[:SS]' (today) in the mess timezone, or RFC 3339. Default: two hours before until, or the task's first event when id is set"`
	Until string `json:"until,omitempty" jsonschema:"end of the window, exclusive, in the same forms as since. Default: now"`
	Limit int    `json:"limit,omitempty" jsonschema:"most events to return, earliest first; default 500, at most 2000"`
}

type SellEventOut struct {
	Task  int64   `json:"task"`
	At    string  `json:"at"`
	Kind  string  `json:"kind" jsonschema:"posted, post_failed, joined, read, image, bot_said, agreed, prompt, parked, passed, pass_counter, timed_out, outbid, revived, payment, pay_unreadable, recovery, pay_mismatch, qr_sent, told_sold, ticked, ended or prices"`
	Group string  `json:"group,omitempty" jsonschema:"the group, for posted and post_failed"`
	Buyer *Person `json:"buyer,omitempty"`
	// Amount is set for kinds whose number is a price; Count for the rest.
	Amount string `json:"amount,omitempty" jsonschema:"INR: the buyer's offer (read), the bot's price (bot_said), the agreed or asked price, the payment, or the new floor (prices)"`
	Count  int64  `json:"count,omitempty" jsonschema:"posts ticked (ticked) or the recovery attempt (recovery)"`
	Text   string `json:"text,omitempty" jsonschema:"the bot's own message, the intent it read, a send error, why a buyer was parked, the task's final state, or the old and new range (prices)"`
}

// SellEventTaskOut is a task with events in the window, as it stands now.
type SellEventTaskOut struct {
	ID      int64  `json:"id"`
	Date    string `json:"date"`
	Meal    string `json:"meal"`
	State   string `json:"state"`
	Detail  string `json:"detail,omitempty"`
	Started string `json:"started"`
	Ended   string `json:"ended,omitempty"`
}

type SellEventsOut struct {
	Now    string             `json:"now"`
	Since  string             `json:"since,omitempty"`
	Until  string             `json:"until"`
	Events []SellEventOut     `json:"events"`
	More   bool               `json:"more,omitempty" jsonschema:"more events matched; ask again with since set after the last one"`
	Tasks  []SellEventTaskOut `json:"tasks"`
}

// priceKinds are the event kinds whose N is paise.
var priceKinds = []string{store.EventRead, store.EventBotSaid, store.EventAgreed, store.EventPrompt, store.EventPayment, store.EventPayMismatch, store.EventPassCounter, store.EventRevived, store.EventPrices, store.EventSoldByYou}

func (s *Server) sellTaskEvents(ctx context.Context, req *mcp.CallToolRequest, in SellEventsIn) (*mcp.CallToolResult, SellEventsOut, error) {
	if _, err := s.sellAdmin(ctx, req); err != nil {
		return nil, SellEventsOut{}, err
	}
	now := s.now()
	until, err := s.parseWhen(in.Until, now)
	if err != nil {
		return nil, SellEventsOut{}, fmt.Errorf("until: %w", err)
	}
	if until.IsZero() {
		until = now
	}
	since, err := s.parseWhen(in.Since, now)
	if err != nil {
		return nil, SellEventsOut{}, fmt.Errorf("since: %w", err)
	}
	if since.IsZero() && in.ID == 0 {
		since = until.Add(-sellEventsWindow)
	}
	if !since.IsZero() && !since.Before(until) {
		return nil, SellEventsOut{}, fmt.Errorf("since (%s) is not before until (%s)", s.fmtSec(since), s.fmtSec(until))
	}
	if in.ID != 0 {
		if t, err := s.st.GetSellTask(ctx, in.ID); err != nil {
			return nil, SellEventsOut{}, err
		} else if t == nil {
			return nil, SellEventsOut{}, fmt.Errorf("no sell task %d", in.ID)
		}
	}
	events, more, err := s.st.SellEventsBetween(ctx, in.ID, since, until, clamp(in.Limit, sellEventsLimit, sellEventsMax))
	if err != nil {
		return nil, SellEventsOut{}, err
	}
	out := SellEventsOut{Now: s.fmtSec(now), Since: s.fmtSec(since), Until: s.fmtSec(until), Events: []SellEventOut{}, More: more, Tasks: []SellEventTaskOut{}}
	names, _ := s.st.GroupNames(ctx)
	people := map[string]*Person{}
	var ids []int64
	for _, e := range events {
		o := SellEventOut{Task: e.TaskID, At: s.fmtSec(e.At), Kind: e.Kind, Text: e.Text}
		switch {
		case strings.HasSuffix(e.Peer, "@g.us"):
			o.Group = groupName(names, e.Peer)
		case e.Peer != "":
			p, ok := people[e.Peer]
			if !ok {
				pp := s.person(ctx, e.Peer, "", s.st.PeerName(ctx, e.Peer))
				p = &pp
				people[e.Peer] = p
			}
			o.Buyer = p
		}
		if slices.Contains(priceKinds, e.Kind) {
			if e.N != 0 {
				o.Amount = store.FormatPaise(e.N)
			}
		} else {
			o.Count = e.N
		}
		out.Events = append(out.Events, o)
		if !slices.Contains(ids, e.TaskID) {
			ids = append(ids, e.TaskID)
		}
	}
	if in.ID != 0 && len(ids) == 0 {
		ids = append(ids, in.ID)
	}
	for _, id := range ids {
		t, err := s.st.GetSellTask(ctx, id)
		if err != nil {
			return nil, SellEventsOut{}, err
		}
		if t == nil {
			continue
		}
		out.Tasks = append(out.Tasks, SellEventTaskOut{ID: t.ID, Date: t.Date, Meal: t.Meal, State: t.State, Detail: t.Detail,
			Started: s.fmtSec(t.CreatedAt), Ended: s.fmtSec(t.EndedAt)})
	}
	return nil, out, nil
}

// parseWhen reads a time an agent passes: RFC 3339, or a date and clock
// time or a clock time alone (today) in the mess timezone. Empty is zero.
func (s *Server) parseWhen(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	loc := s.sched.Location()
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if c, err := time.ParseInLocation(layout, v, loc); err == nil {
			y, m, d := now.In(loc).Date()
			return time.Date(y, m, d, c.Hour(), c.Minute(), c.Second(), 0, loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time; use 'YYYY-MM-DD HH:MM', 'HH:MM' or RFC 3339", v)
}

// fmtSec is fmt with seconds, for events that come seconds apart.
func (s *Server) fmtSec(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.sched.Location()).Format("2006-01-02 15:04:05 MST")
}
