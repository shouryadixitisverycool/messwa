package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/repost"
	"messwa/internal/store"
)

// SellTasks starts and cancels sell tasks inside run. The rules a task
// follows (who it may message, the floor price, rate limits, the meal's end)
// are enforced there, not by these tools.
type SellTasks interface {
	Start(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, handoff string) (int64, error)
	Cancel(ctx context.Context, id int64) error
}

// SetSellTasks adds the sell task tools when [mcp] sell_tasks is on.
func (s *Server) SetSellTasks(t SellTasks) { s.sell = t }

func (s *Server) sellEnabled() bool { return s.sell != nil && s.cfg.MCP.SellTasks }

const sellInstructions = `

Sell tasks (admins only) sell one of your registered meals through WhatsApp. start_sell_task checks the registration and posts an offer in the mess groups set in config.toml. With [sell] accept_plain_dms in config.toml, people who DM about the offer without quoting it count as replies too. When config.toml sets [sell] buyers = "unsaved_only", replies from saved contacts are skipped and they get nothing. With max_price above min_price, the task negotiates with everyone who replies privately to a post, all at once: it opens at max_price, says it's slightly negotiable, and comes down toward the meal's usual price, and toward min_price as the meal's end nears. It never quotes, agrees or accepts less than min_price. Buyers who agree pay one at a time; the first matching WhatsApp Pay payment gets the meal's QR, and the offer posts get a ✅. With max_price equal to min_price it just asks that price. Buyers the bot can't handle (unreadable messages, still too low after its lowest price, wanting to pay another way) are listed in needs_you, along with buyers whose payment it could not honour and the amount to refund. With handoff "on_reply" the task stops at the first private reply and sends that buyer nothing; with "on_agreement" it negotiates and hands over the first buyer who agrees, at the agreed price. sell_task_status shows who to message. While the task is active it reposts the offer in a group once the last copy has dropped out of view (after [sell] repost_after, 10 minutes by default, or sooner once enough messages or other sellers' listings for the same meal and mess came in after it), up to max_reposts per group and never in the meal's last 15 minutes. Replies to any copy count. sell_task_status lists every copy and when each group gets its next one, and why. A task ends when the meal sells, when it hands off, when it is cancelled, or when the meal ends. Check on it with sell_task_status and stop it with cancel_sell_task. Start a task only when the user asked you to sell that meal, at the prices they gave.`

func (s *Server) addSellTools(srv *mcp.Server) {
	write := &mcp.ToolAnnotations{OpenWorldHint: ptr(true)}
	mcp.AddTool(srv, &mcp.Tool{Name: "start_sell_task", Annotations: write,
		Description: "Sell one of your registered meals: post an offer in the mess group, negotiate between min_price and max_price with buyers who reply privately, and send the QR after a matching payment. Prices are whole rupees. Admins only."}, s.startSellTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "sell_task_status", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "One sell task by id, or every active task plus those started in the last 24 hours: state, every copy of the offer posted (reposts included, marking each group's current one), when each group gets its next fresh copy and why, buyers who replied, and the sale. Admins only."}, s.sellTaskStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "cancel_sell_task", Annotations: write,
		Description: "Stop a sell task. A QR already being delivered for a payment still goes out, and the task then shows as sold. Admins only."}, s.cancelSellTask)
}

func ptr[T any](v T) *T { return &v }

type StartSellIn struct {
	Date     string `json:"date" jsonschema:"meal date, YYYY-MM-DD"`
	Meal     string `json:"meal" jsonschema:"breakfast, lunch, snacks or dinner"`
	MinPrice int    `json:"min_price" jsonschema:"lowest price in rupees to accept; never quoted or accepted below this"`
	MaxPrice int    `json:"max_price" jsonschema:"price in rupees to open at; above min_price the task negotiates down from it"`
	Handoff  string `json:"handoff,omitempty" jsonschema:"never (default): the task runs the whole sale. on_reply: the first private reply ends the task and hands that buyer's chat to the user; the bot sends them nothing. on_agreement: the task negotiates, then hands over the first buyer who agrees, at the agreed price, without asking them to pay"`
}

type SellTaskIn struct {
	ID int64 `json:"id,omitempty" jsonschema:"task id; omit for every active and recent task"`
}

type CancelSellIn struct {
	ID int64 `json:"id"`
}

type SellOfferOut struct {
	Group    string `json:"group"`
	Copy     int    `json:"copy" jsonschema:"0 for the first copy in this group, then 1, 2... for each repost"`
	Current  bool   `json:"current,omitempty" jsonschema:"the group's latest copy, the one buyers see"`
	State    string `json:"state" jsonschema:"posting (sent, not confirmed), posted or failed"`
	PostedAt string `json:"posted_at"`
}

// SellRepostOut is when a group gets its next fresh copy of the offer.
type SellRepostOut struct {
	Group   string `json:"group"`
	Reposts int    `json:"reposts" jsonschema:"fresh copies posted in this group so far"`
	// Counted after the current copy, from the group log.
	MessagesAfter int    `json:"messages_after,omitempty" jsonschema:"messages that came in after the current copy"`
	ListingsAfter int    `json:"listings_after,omitempty" jsonschema:"other sellers' listings for this meal at this mess after the current copy"`
	Due           bool   `json:"due,omitempty" jsonschema:"a fresh copy goes out within the next half minute"`
	NextAt        string `json:"next_at,omitempty" jsonschema:"when the next copy goes out unless a message or listing count brings it forward"`
	Why           string `json:"why"`
	Stopped       bool   `json:"stopped,omitempty" jsonschema:"this group gets no more copies; why says why"`
}

type SellBuyerOut struct {
	Person
	State    string `json:"state" jsonschema:"queued, negotiating, agreed (waiting for their turn to pay), offered (asked to pay), passed, timed_out, bought, skipped, told_sold, parked (needs the user), or handed_off"`
	Asked    string `json:"asked,omitempty" jsonschema:"price asked in the payment prompt, INR"`
	PayUntil string `json:"pay_until,omitempty"`
	// Negotiation, for a task with a price range.
	Ask       string `json:"ask,omitempty" jsonschema:"the bot's latest price to this buyer, INR"`
	LastOffer string `json:"last_offer,omitempty" jsonschema:"the buyer's latest offer, INR"`
	Rounds    int    `json:"rounds,omitempty" jsonschema:"counter-offers the bot made"`
	Agreed    string `json:"agreed,omitempty" jsonschema:"price agreed, INR"`
	Note      string `json:"note,omitempty" jsonschema:"why the buyer needs the user"`
}

// SellNeedsYouOut is a buyer the bot stopped talking to.
type SellNeedsYouOut struct {
	Buyer     Person `json:"buyer"`
	ChatLink  string `json:"chat_link,omitempty" jsonschema:"wa.me link to the buyer's chat; empty when WhatsApp hides their number"`
	Why       string `json:"why"`
	Ask       string `json:"ask,omitempty" jsonschema:"the bot's latest price to them, INR"`
	LastOffer string `json:"last_offer,omitempty" jsonschema:"their latest offer, INR"`
	Refund    string `json:"refund,omitempty" jsonschema:"a payment the bot could not honour, INR; send it back unless they get the meal"`
}

type SellSaleOut struct {
	Buyer  string `json:"buyer"`
	Amount string `json:"amount"`
	State  string `json:"state" jsonschema:"armed (waiting for payment), sending (payment claimed, QR going out), sent, or handed_off"`
}

// SellHandoffOut is the buyer a task handed to the user.
type SellHandoffOut struct {
	Buyer    Person   `json:"buyer"`
	ChatLink string   `json:"chat_link,omitempty" jsonschema:"wa.me link to the buyer's chat; empty when WhatsApp hides their number"`
	Asked    string   `json:"asked" jsonschema:"price the offer asked, INR; with on_agreement, the price the buyer agreed"`
	Others   []Person `json:"also_replied" jsonschema:"other buyers who replied before the handoff, in reply order"`
	// Finish is what the user runs once they agree a price, with run stopped.
	Finish string `json:"finish" jsonschema:"how to finish or drop the sale from the command line"`
}

type SellTaskOut struct {
	ID       int64          `json:"id"`
	Date     string         `json:"date"`
	Meal     string         `json:"meal"`
	Mess     string         `json:"mess,omitempty"`
	Diet     string         `json:"diet,omitempty"`
	State    string         `json:"state" jsonschema:"active, sold, handed_off, cancelled, expired, or failed"`
	Detail   string         `json:"detail,omitempty"`
	MinPrice string         `json:"min_price"`
	MaxPrice string         `json:"max_price"`
	Handoff  string         `json:"handoff" jsonschema:"never, on_reply or on_agreement"`
	Started  string         `json:"started"`
	Ends     string         `json:"ends"`
	Ended    string         `json:"ended,omitempty"`
	Offers   []SellOfferOut `json:"offers"`
	// Reposts, per group, while the task is active.
	Reposts []SellRepostOut `json:"reposts,omitempty"`
	Buyers  []SellBuyerOut  `json:"buyers"`
	Sale    *SellSaleOut    `json:"sale,omitempty"`
	// NeedsYou are buyers the bot handed to the user without ending the
	// task, and buyers who paid when the bot could not sell to them.
	NeedsYou []SellNeedsYouOut `json:"needs_you,omitempty"`
	// HandedOff is set once the task handed a buyer to the user.
	HandedOff *SellHandoffOut `json:"handed_off,omitempty"`
}

type SellTasksOut struct {
	Now   string        `json:"now"`
	Tasks []SellTaskOut `json:"tasks"`
}

func (s *Server) sellAdmin(ctx context.Context, req *mcp.CallToolRequest) (int64, error) {
	if !s.sellEnabled() {
		return 0, errors.New("sell tasks are turned off; set [mcp] sell_tasks = true in config.toml")
	}
	u, err := s.caller(ctx, req)
	if err != nil {
		return 0, err
	}
	if !u.IsAdmin() {
		return 0, errors.New("only admins can run sell tasks for the linked WhatsApp account")
	}
	return u.ID, nil
}

func (s *Server) startSellTask(ctx context.Context, req *mcp.CallToolRequest, in StartSellIn) (*mcp.CallToolResult, SellTaskOut, error) {
	uid, err := s.sellAdmin(ctx, req)
	if err != nil {
		return nil, SellTaskOut{}, err
	}
	id, err := s.sell.Start(ctx, uid, in.Date, in.Meal, in.MinPrice, in.MaxPrice, in.Handoff)
	if err != nil {
		return nil, SellTaskOut{}, err
	}
	out, err := s.sellTask(ctx, id)
	return nil, out, err
}

func (s *Server) sellTaskStatus(ctx context.Context, req *mcp.CallToolRequest, in SellTaskIn) (*mcp.CallToolResult, SellTasksOut, error) {
	if _, err := s.sellAdmin(ctx, req); err != nil {
		return nil, SellTasksOut{}, err
	}
	out := SellTasksOut{Now: s.fmt(s.now()), Tasks: []SellTaskOut{}}
	if in.ID != 0 {
		t, err := s.sellTask(ctx, in.ID)
		if err != nil {
			return nil, SellTasksOut{}, err
		}
		out.Tasks = append(out.Tasks, t)
		return nil, out, nil
	}
	tasks, err := s.st.SellTasks(ctx, s.now().Add(-24*time.Hour))
	if err != nil {
		return nil, SellTasksOut{}, err
	}
	for _, t := range tasks {
		o, err := s.sellTaskOut(ctx, t)
		if err != nil {
			return nil, SellTasksOut{}, err
		}
		out.Tasks = append(out.Tasks, o)
	}
	return nil, out, nil
}

func (s *Server) cancelSellTask(ctx context.Context, req *mcp.CallToolRequest, in CancelSellIn) (*mcp.CallToolResult, SellTaskOut, error) {
	if _, err := s.sellAdmin(ctx, req); err != nil {
		return nil, SellTaskOut{}, err
	}
	if err := s.sell.Cancel(ctx, in.ID); err != nil {
		return nil, SellTaskOut{}, err
	}
	out, err := s.sellTask(ctx, in.ID)
	return nil, out, err
}

func (s *Server) sellTask(ctx context.Context, id int64) (SellTaskOut, error) {
	t, err := s.st.GetSellTask(ctx, id)
	if err != nil {
		return SellTaskOut{}, err
	}
	if t == nil {
		return SellTaskOut{}, fmt.Errorf("no sell task %d", id)
	}
	return s.sellTaskOut(ctx, *t)
}

// nextRepost is the same decision the running task makes about a group's
// copies, from the same group log.
func (s *Server) nextRepost(ctx context.Context, rules repost.Rules, group string, copies []repost.Copy, end time.Time) SellRepostOut {
	current := copies[0]
	for _, c := range copies {
		if c.Seq > current.Seq {
			current = c
		}
	}
	var act repost.Activity
	if current.State != repost.Failed {
		if a, known, err := s.st.After(ctx, current.Group, current.ID); err == nil {
			act = repost.Activity{Known: known, Messages: a.Messages, Listings: a.Listings}
		}
	}
	n := repost.Plan(rules, copies, act, s.now(), end)
	return SellRepostOut{Group: group, Reposts: n.Reposts, MessagesAfter: n.Messages, ListingsAfter: n.Listings,
		Due: n.Due, NextAt: s.fmt(n.At), Why: n.Why(rules), Stopped: n.Stopped != ""}
}

func (s *Server) sellTaskOut(ctx context.Context, t store.SellTask) (SellTaskOut, error) {
	out := SellTaskOut{
		ID: t.ID, Date: t.Date, Meal: t.Meal, Mess: t.Mess, Diet: t.Diet, State: t.State, Detail: t.Detail,
		MinPrice: store.FormatPaise(t.MinPaise), MaxPrice: store.FormatPaise(t.MaxPaise), Handoff: t.Handoff,
		Started: s.fmt(t.CreatedAt), Ends: s.fmt(t.EndsAt), Ended: s.fmt(t.EndedAt),
		Offers: []SellOfferOut{}, Buyers: []SellBuyerOut{},
	}
	offers, err := s.st.SellTaskOffers(ctx, t.ID)
	if err != nil {
		return out, err
	}
	names, _ := s.st.GroupNames(ctx)
	latest := map[string]int{}
	var order []string
	byGroup := map[string][]repost.Copy{}
	for _, o := range offers {
		if _, ok := latest[o.Group]; !ok {
			order = append(order, o.Group)
		}
		latest[o.Group] = max(latest[o.Group], o.Seq)
		byGroup[o.Group] = append(byGroup[o.Group], repost.Copy{Group: o.Group, Seq: o.Seq, ID: o.ID, At: o.PostedAt, State: o.State})
	}
	for _, o := range offers {
		out.Offers = append(out.Offers, SellOfferOut{Group: groupName(names, o.Group), Copy: o.Seq, Current: o.Seq == latest[o.Group],
			State: o.State, PostedAt: s.fmt(o.PostedAt)})
	}
	if t.State == store.TaskActive {
		rules := repost.FromConfig(s.cfg.Sell)
		for _, g := range order {
			out.Reposts = append(out.Reposts, s.nextRepost(ctx, rules, groupName(names, g), byGroup[g], t.EndsAt))
		}
	}
	buyers, err := s.st.SellTaskBuyers(ctx, t.ID)
	if err != nil {
		return out, err
	}
	inr := func(paise int64) string {
		if paise <= 0 {
			return ""
		}
		return store.FormatPaise(paise)
	}
	for _, b := range buyers {
		p := s.person(ctx, b.Peer, b.Alt, "")
		bo := SellBuyerOut{Person: p, State: b.State, Asked: b.Amount, PayUntil: s.fmt(b.PayUntil),
			Rounds: b.Rounds, Agreed: b.Agreed, Note: b.Note, LastOffer: inr(b.LastOffer)}
		if b.BotMsgs > 0 {
			bo.Ask = inr(b.Ask)
		}
		out.Buyers = append(out.Buyers, bo)
		if b.Note != "" || b.Refund != "" {
			n := SellNeedsYouOut{Buyer: p, Why: b.Note, Ask: bo.Ask, LastOffer: bo.LastOffer, Refund: b.Refund}
			if p.Phone != "" {
				n.ChatLink = "https://wa.me/" + strings.TrimPrefix(p.Phone, "+")
			}
			out.NeedsYou = append(out.NeedsYou, n)
		}
	}
	d, state, err := s.st.TaskDelivery(ctx, t.ID)
	if err != nil {
		return out, err
	}
	if d != nil {
		out.Sale = &SellSaleOut{Buyer: d.Peer, Amount: d.Amount, State: state}
	}
	if d != nil && state == "handed_off" {
		name, err := s.st.TaskBuyerName(ctx, t.ID)
		if err != nil {
			return out, err
		}
		h := &SellHandoffOut{Asked: d.Amount, Others: []Person{},
			Finish: fmt.Sprintf("Agree a price in the chat, stop run, then: messwa deliver-qr -date %s -meal %s -peer %s -amount PRICE -portal-qr (add -user NAME unless the meal is the first admin's). To drop the sale: messwa release-handoff -date %s -meal %s, which is safe while run is up.", d.Date, d.Meal, d.Peer, d.Date, d.Meal)}
		for _, b := range buyers {
			switch {
			case b.Peer == d.Peer:
				h.Buyer = s.person(ctx, b.Peer, b.Alt, name)
			case b.State == store.BuyerQueued || b.State == store.BuyerNegotiating || b.State == store.BuyerAgreed:
				h.Others = append(h.Others, s.person(ctx, b.Peer, b.Alt, ""))
			}
		}
		if h.Buyer.JID == "" {
			h.Buyer = s.person(ctx, d.Peer, "", name)
		}
		if h.Buyer.Phone != "" {
			h.ChatLink = "https://wa.me/" + strings.TrimPrefix(h.Buyer.Phone, "+")
		}
		out.HandedOff = h
	}
	return out, nil
}
