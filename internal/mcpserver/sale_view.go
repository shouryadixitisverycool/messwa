package mcpserver

import (
	"context"
	"strings"
	"time"

	"messwa/internal/market"
	"messwa/internal/store"
)

// saleViewLinger is how long a finished task stays on the board, so the
// result of a sale is still there when you look.
const saleViewLinger = 20 * time.Minute

// SaleView is one sell task as the board's "my sale" chest draws it.
// Prices are rupees. Buyer events hold what the bot read and said, never
// what a buyer wrote (see store.SellEvent).
type SaleView struct {
	ID   int64  `json:"id"`
	Date string `json:"date"`
	Meal string `json:"meal"`
	// BaseMeal is Meal without a slot suffix: "dinner" for "dinner#f1".
	BaseMeal string `json:"base_meal"`
	// Friend is set for a friend's meal: their name, or "" when none was
	// given. ForFriend says it is one.
	Friend    string `json:"friend,omitempty"`
	ForFriend bool   `json:"for_friend,omitempty"`
	Mess      string `json:"mess,omitempty"`
	Diet      string `json:"diet,omitempty"`
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	Handoff   string `json:"handoff"`
	// Min is the floor and Max the opening price. Target is what the
	// negotiation aims for. Lowest is what the bot accepts right now; Held
	// says it is held at Target because two or more buyers are active.
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
	Target     float64 `json:"target"`
	Lowest     float64 `json:"lowest"`
	Held       bool    `json:"held,omitempty"`
	Negotiates bool    `json:"negotiates"`
	Started    int64   `json:"started"`
	Ends       int64   `json:"ends"`
	Ended      int64   `json:"ended,omitempty"`

	Posts  []SalePost  `json:"posts"`
	Buyers []SaleBuyer `json:"buyers"`
	Pay    *SalePay    `json:"pay,omitempty"`
	// Ticked is how many offer posts got their ✅ after the sale.
	Ticked  int       `json:"ticked,omitempty"`
	History []SaleTry `json:"history"`
	// Listings are the board listings your offer posts became.
	Listings []int64 `json:"listings"`
}

type SalePost struct {
	Group string `json:"group"`
	JID   string `json:"jid"`
	// Seq is the copy number in its group: 0 for the first, then one per
	// repost or resend.
	Seq   int    `json:"seq"`
	State string `json:"state"` // posting, posted, failed
	At    int64  `json:"at"`
	Error string `json:"error,omitempty"`
	// Revoked is when a cancelled task deleted the post for everyone.
	Revoked int64 `json:"revoked,omitempty"`
	// After is how many listing posts went up in the group after yours.
	After int `json:"after"`
	// Resend is set on a failed copy that is its group's latest while the
	// task is active, so you can post it again.
	Resend bool `json:"resend,omitempty"`
}

type SaleBuyer struct {
	// Key is their phone number when known, else their JID, like the sales
	// page, so a buyer has the same pixel head on both.
	Key      string      `json:"key"`
	Name     string      `json:"name,omitempty"` // untrusted
	Phone    string      `json:"phone,omitempty"`
	Saved    bool        `json:"saved"`
	State    string      `json:"state"`
	Joined   int64       `json:"joined"`
	Ask      float64     `json:"ask,omitempty"`
	Offer    float64     `json:"offer,omitempty"`
	Agreed   float64     `json:"agreed,omitempty"`
	Asked    float64     `json:"asked,omitempty"`
	PayUntil int64       `json:"pay_until,omitempty"`
	BotMsgs  int         `json:"bot_msgs"`
	Note     string      `json:"note,omitempty"`
	Refund   float64     `json:"refund,omitempty"`
	Muted    bool        `json:"muted,omitempty"` // the bot sends them nothing
	Events   []SaleEvent `json:"events"`
}

type SaleEvent struct {
	At   int64  `json:"at"`
	Kind string `json:"kind"`
	// N is rupees for prices, the attempt for a payment recovery.
	N    float64 `json:"n,omitempty"`
	Text string  `json:"text,omitempty"`
}

type SalePay struct {
	Buyer  string  `json:"buyer"` // a SaleBuyer key
	Amount float64 `json:"amount"`
	State  string  `json:"state"` // armed, sending, sent, handed_off
	Armed  int64   `json:"armed"`
	Paid   int64   `json:"paid,omitempty"`
	Sent   int64   `json:"sent,omitempty"`
}

type SaleTry struct {
	ID      int64  `json:"id"`
	State   string `json:"state"`
	Started int64  `json:"started"`
	Ended   int64  `json:"ended,omitempty"`
	Buyers  int    `json:"buyers"`
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func rupeesOf(paise int64) float64 { return float64(paise) / 100 }

func rupeesStr(amount string) float64 {
	p, err := store.ParsePaise(amount)
	if err != nil {
		return 0
	}
	return rupeesOf(p)
}

// SaleViews returns the sell tasks the board shows: active ones, and ones
// that ended in the last saleViewLinger.
func (s *Server) SaleViews(ctx context.Context) ([]SaleView, error) {
	now := s.now()
	tasks, err := s.st.SellTasks(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	out := []SaleView{}
	for _, t := range tasks {
		if t.State != store.TaskActive && now.Sub(t.EndedAt) > saleViewLinger {
			continue
		}
		v, err := s.saleView(ctx, t, now)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) saleView(ctx context.Context, t store.SellTask, now time.Time) (SaleView, error) {
	v := SaleView{ID: t.ID, Date: t.Date, Meal: t.Meal, Mess: t.Mess, Diet: t.Diet, State: t.State, Detail: t.Detail, Handoff: t.Handoff,
		Min: rupeesOf(t.MinPaise), Max: rupeesOf(t.MaxPaise), Started: unix(t.CreatedAt), Ends: unix(t.EndsAt), Ended: unix(t.EndedAt),
		Posts: []SalePost{}, Buyers: []SaleBuyer{}, History: []SaleTry{}, Listings: []int64{}}
	v.BaseMeal, _, _ = strings.Cut(t.Meal, "#")
	if store.IsFriendSlot(t.Meal) {
		v.ForFriend = true
		if f, err := s.st.FriendMeal(ctx, t.Date, t.Meal); err == nil && f != nil {
			v.Friend = f.Friend
		}
	}

	names, _ := s.st.GroupNames(ctx)
	offers, err := s.st.SellTaskOffers(ctx, t.ID)
	if err != nil {
		return v, err
	}
	latest := map[string]int{}
	for _, o := range offers {
		latest[o.Group] = max(latest[o.Group], o.Seq)
	}
	for _, o := range offers {
		p := SalePost{Group: groupName(names, o.Group), JID: o.Group, Seq: o.Seq, State: o.State,
			Resend: t.State == store.TaskActive && o.State == store.OfferFailed && o.Seq == latest[o.Group], At: unix(o.PostedAt), Error: o.Error, Revoked: unix(o.RevokedAt)}
		if o.State == store.OfferPosted {
			if p.After, err = s.st.PostsAfter(ctx, o.Group, o.ID, o.PostedAt); err != nil {
				return v, err
			}
			if l, err := s.st.PostListing(ctx, o.Group, o.ID); err == nil && l != nil {
				v.Listings = append(v.Listings, l.ID)
			}
		}
		v.Posts = append(v.Posts, p)
	}

	buyers, err := s.st.SellTaskBuyers(ctx, t.ID)
	if err != nil {
		return v, err
	}
	events, err := s.st.SellTaskEvents(ctx, t.ID)
	if err != nil {
		return v, err
	}
	active := 0
	keys := map[string]string{}
	for _, b := range buyers {
		p := s.person(ctx, b.Peer, b.Alt, b.Name)
		if p.Name == "" {
			p.Name = s.st.PeerName(ctx, b.Peer, b.Alt)
		}
		sb := SaleBuyer{Key: firstOf(p.Phone, b.Peer), Name: p.Name, Phone: p.Phone, Saved: p.Saved, State: b.State,
			Joined: b.JoinedAt.Unix(), Offer: rupeesOf(b.LastOffer), Agreed: rupeesStr(b.Agreed), Asked: rupeesStr(b.Amount),
			PayUntil: unix(b.PayUntil), BotMsgs: b.BotMsgs, Note: b.Note, Refund: rupeesStr(b.Refund), Muted: b.Muted, Events: []SaleEvent{}}
		if b.BotMsgs > 0 {
			sb.Ask = rupeesOf(b.Ask)
		}
		for _, e := range events {
			if e.Peer == "" || (e.Peer != b.Peer && e.Peer != b.Alt) {
				continue
			}
			n := rupeesOf(e.N)
			if e.Kind == store.EventRecovery {
				n = float64(e.N)
			}
			sb.Events = append(sb.Events, SaleEvent{At: e.At.Unix(), Kind: e.Kind, N: n, Text: e.Text})
		}
		keys[b.Peer] = sb.Key
		switch b.State {
		case store.BuyerNegotiating, store.BuyerAgreed, store.BuyerOffered:
			active++
		}
		v.Buyers = append(v.Buyers, sb)
	}
	for _, e := range events {
		if e.Kind == store.EventTicked {
			v.Ticked += int(e.N)
		}
	}

	// The price the bot takes now, as negotiate.go works it out.
	maxR, minR := int(t.MaxPaise/100), int(t.MinPaise/100)
	v.Negotiates = t.MaxPaise > t.MinPaise && t.Handoff != "on_reply"
	v.Target, v.Lowest = v.Max, v.Max
	if v.Negotiates {
		target := maxR
		if t.TargetPaise > 0 {
			target = int(t.TargetPaise / 100)
		}
		unit := market.PriceUnit(maxR)
		v.Target = float64(target)
		v.Lowest = float64(market.Walk(minR, target, unit, t.EndsAt, now, active >= 2))
		v.Held = active >= 2 && market.Walk(minR, target, unit, t.EndsAt, now, false) < target
	}

	d, dstate, err := s.st.TaskDelivery(ctx, t.ID)
	if err != nil {
		return v, err
	}
	if d != nil {
		paid, sent, err := s.st.TaskDeliveryTimes(ctx, t.ID)
		if err != nil {
			return v, err
		}
		key := keys[d.Peer]
		if key == "" {
			key = d.Peer
			if user, ok := strings.CutSuffix(d.Peer, "@s.whatsapp.net"); ok {
				key = "+" + user
			}
		}
		v.Pay = &SalePay{Buyer: key, Amount: rupeesStr(d.Amount), State: dstate, Armed: unix(d.ArmedAt), Paid: unix(paid), Sent: unix(sent)}
	}

	tries, err := s.st.SellTasksForMeal(ctx, t.Date, t.Meal)
	if err != nil {
		return v, err
	}
	for _, x := range tries {
		bs, err := s.st.SellTaskBuyers(ctx, x.ID)
		if err != nil {
			return v, err
		}
		v.History = append(v.History, SaleTry{ID: x.ID, State: x.State, Started: unix(x.CreatedAt), Ended: unix(x.EndedAt), Buyers: len(bs)})
	}
	return v, nil
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
