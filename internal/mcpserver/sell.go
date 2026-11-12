package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

Sell tasks (admins only) sell one of your registered meals through WhatsApp. start_sell_task checks the registration, posts an offer in the mess groups set in config.toml and asks everyone who replies privately to a post to pay, one at a time. With [sell] accept_plain_dms in config.toml, people who DM about the offer without quoting it are asked too. The first matching WhatsApp Pay payment gets the meal's QR, and the offer posts get a ✅. The task asks max_price; it never asks for or accepts less than min_price. When config.toml sets [sell] buyers = "unsaved_only", replies from saved contacts are skipped and they get nothing. With handoff "on_reply" the task instead stops at the first private reply, sends that buyer nothing, and sell_task_status shows who to message. It ends when the meal sells, when it hands off, when it is cancelled, or when the meal ends. Check on it with sell_task_status and stop it with cancel_sell_task. Start a task only when the user asked you to sell that meal, at the prices they gave.`

func (s *Server) addSellTools(srv *mcp.Server) {
	write := &mcp.ToolAnnotations{OpenWorldHint: ptr(true)}
	mcp.AddTool(srv, &mcp.Tool{Name: "start_sell_task", Annotations: write,
		Description: "Sell one of your registered meals: post an offer in the mess group, ask buyers who reply privately to pay, and send the QR after a matching payment. Prices are whole rupees. Admins only."}, s.startSellTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "sell_task_status", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "One sell task by id, or every active task plus those started in the last 24 hours: state, offer posts, buyers who replied, and the sale. Admins only."}, s.sellTaskStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "cancel_sell_task", Annotations: write,
		Description: "Stop a sell task. A QR already being delivered for a payment still goes out, and the task then shows as sold. Admins only."}, s.cancelSellTask)
}

func ptr[T any](v T) *T { return &v }

type StartSellIn struct {
	Date     string `json:"date" jsonschema:"meal date, YYYY-MM-DD"`
	Meal     string `json:"meal" jsonschema:"breakfast, lunch, snacks or dinner"`
	MinPrice int    `json:"min_price" jsonschema:"lowest price in rupees to accept; never asked or accepted below this"`
	MaxPrice int    `json:"max_price" jsonschema:"price in rupees to ask first"`
	Handoff  string `json:"handoff,omitempty" jsonschema:"never (default): the task runs the whole sale. on_reply: the first private reply ends the task and hands that buyer's chat to the user; the bot sends them nothing"`
}

type SellTaskIn struct {
	ID int64 `json:"id,omitempty" jsonschema:"task id; omit for every active and recent task"`
}

type CancelSellIn struct {
	ID int64 `json:"id"`
}

type SellOfferOut struct {
	Group    string `json:"group"`
	State    string `json:"state" jsonschema:"posting (sent, not confirmed), posted or failed"`
	PostedAt string `json:"posted_at"`
}

type SellBuyerOut struct {
	Person
	State    string `json:"state" jsonschema:"queued, offered (asked to pay), passed, timed_out, bought, skipped, told_sold, or handed_off"`
	Asked    string `json:"asked,omitempty" jsonschema:"price asked, INR"`
	PayUntil string `json:"pay_until,omitempty"`
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
	Asked    string   `json:"asked" jsonschema:"price the offer asked, INR"`
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
	Handoff  string         `json:"handoff" jsonschema:"never or on_reply"`
	Started  string         `json:"started"`
	Ends     string         `json:"ends"`
	Ended    string         `json:"ended,omitempty"`
	Offers   []SellOfferOut `json:"offers"`
	Buyers   []SellBuyerOut `json:"buyers"`
	Sale     *SellSaleOut   `json:"sale,omitempty"`
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
	for _, o := range offers {
		out.Offers = append(out.Offers, SellOfferOut{Group: groupName(names, o.Group), State: o.State, PostedAt: s.fmt(o.PostedAt)})
	}
	buyers, err := s.st.SellTaskBuyers(ctx, t.ID)
	if err != nil {
		return out, err
	}
	for _, b := range buyers {
		out.Buyers = append(out.Buyers, SellBuyerOut{Person: s.person(ctx, b.Peer, b.Alt, ""), State: b.State, Asked: b.Amount, PayUntil: s.fmt(b.PayUntil)})
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
			case b.State == store.BuyerQueued:
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
