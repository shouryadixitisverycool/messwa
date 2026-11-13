package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/store"
)

// SellOperator is what you can do to a running sell task by hand. The
// dashboard's MY SALE box offers the same actions.
type SellOperator interface {
	// MarkSold records that you sold the meal yourself, to who (a phone
	// number or JID) or name, for priceRupees.
	MarkSold(ctx context.Context, id int64, who, name string, priceRupees int, note string) error
	// AddBuyer queues who as if they had replied privately to the offer.
	AddBuyer(ctx context.Context, id int64, who, name string) error
	// MuteBuyer stops or restarts the bot messaging one of the task's buyers.
	MuteBuyer(ctx context.Context, id int64, who string, muted bool) error
}

// SellControl returns what starts, reprices and cancels sell tasks, while
// sell tasks are on and this account's WhatsApp is linked. The dashboard's
// sales ledger uses it to sell a meal from its slot.
func (s *Server) SellControl() (SellTasks, error) {
	if s.Scoped() && (s.Device == nil || !s.Device.Linked()) {
		return nil, errors.New("link your WhatsApp account first")
	}
	if !s.sellEnabled() {
		return nil, errors.New("sell tasks are turned off; an admin can turn them on in the dashboard's settings")
	}
	return s.sell, nil
}

// SellOperator returns the sell task actions while sell tasks are on.
func (s *Server) SellOperator() (SellOperator, error) {
	sell, err := s.SellControl()
	if err != nil {
		return nil, err
	}
	op, ok := sell.(SellOperator)
	if !ok {
		return nil, errors.New("this messwa can't change running sell tasks")
	}
	return op, nil
}

func (s *Server) addSellActionTools(srv *mcp.Server, write *mcp.ToolAnnotations) {
	mcp.AddTool(srv, &mcp.Tool{Name: "mark_sell_task_sold", Annotations: write,
		Description: "Record that the user sold a running sell task's meal themselves, outside the bot. The task stops, the sale goes in the ledger as paid and sent now (payment kind manual), buyers still in the running get the usual \"sold\" message, and the offer posts get a ✅. The buyer gets no message from the bot. Use it only when the user says they sold the meal, with the buyer and price they gave. Uses your own linked account."}, s.markSellTaskSold)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_sell_task_buyer", Annotations: write,
		Description: "Add someone to a running sell task's buyers as if they had replied privately to its offer. The bot then sends them the opening price (or, without a price range, asks them to pay) like anyone who replied. Fails for someone already a buyer, and for saved contacts when [sell] buyers is \"unsaved_only\". Uses your own linked account."}, s.addSellTaskBuyer)
	mcp.AddTool(srv, &mcp.Tool{Name: "mute_sell_task_buyer", Annotations: write,
		Description: "Stop the bot messaging one of a running sell task's buyers: no replies, no payment prompt, no \"sold\" message. Their messages are ignored and their record stays. Payments received while muted do not trigger automatic QR delivery. Undo with unmute_sell_task_buyer. Uses your own linked account."}, s.muteSellTaskBuyer)
	mcp.AddTool(srv, &mcp.Tool{Name: "unmute_sell_task_buyer", Annotations: write,
		Description: "Let the bot message a muted buyer of a running sell task again. It answers their next message; nothing is sent right away. Uses your own linked account."}, s.unmuteSellTaskBuyer)
}

type MarkSoldIn struct {
	ID    int64  `json:"id" jsonschema:"sell task id"`
	Buyer string `json:"buyer,omitempty" jsonschema:"the buyer's phone number with country code, or WhatsApp JID; one of the task's buyers or anyone else. Omit to give only a name"`
	Name  string `json:"name,omitempty" jsonschema:"the buyer's name; required when buyer is omitted"`
	Price int    `json:"price" jsonschema:"price in whole rupees"`
	Note  string `json:"note,omitempty" jsonschema:"anything worth keeping, like how they paid"`
}

type AddBuyerIn struct {
	ID    int64  `json:"id" jsonschema:"sell task id"`
	Buyer string `json:"buyer" jsonschema:"phone number with country code, or WhatsApp JID"`
	Name  string `json:"name,omitempty"`
}

type MuteBuyerIn struct {
	ID    int64  `json:"id" jsonschema:"sell task id"`
	Buyer string `json:"buyer" jsonschema:"one of the task's buyers: phone number with country code, or WhatsApp JID, as sell_task_status lists them"`
}

func (s *Server) sellAction(ctx context.Context, req *mcp.CallToolRequest, id int64, do func(SellOperator) error) (*mcp.CallToolResult, SellTaskOut, error) {
	if _, err := s.sellAdmin(ctx, req); err != nil {
		return nil, SellTaskOut{}, err
	}
	op, err := s.SellOperator()
	if err != nil {
		return nil, SellTaskOut{}, err
	}
	if err := do(op); err != nil {
		return nil, SellTaskOut{}, err
	}
	out, err := s.sellTask(ctx, id)
	return nil, out, err
}

func (s *Server) markSellTaskSold(ctx context.Context, req *mcp.CallToolRequest, in MarkSoldIn) (*mcp.CallToolResult, SellTaskOut, error) {
	return s.sellAction(ctx, req, in.ID, func(op SellOperator) error {
		return op.MarkSold(ctx, in.ID, in.Buyer, in.Name, in.Price, in.Note)
	})
}

func (s *Server) addSellTaskBuyer(ctx context.Context, req *mcp.CallToolRequest, in AddBuyerIn) (*mcp.CallToolResult, SellTaskOut, error) {
	return s.sellAction(ctx, req, in.ID, func(op SellOperator) error { return op.AddBuyer(ctx, in.ID, in.Buyer, in.Name) })
}

func (s *Server) muteSellTaskBuyer(ctx context.Context, req *mcp.CallToolRequest, in MuteBuyerIn) (*mcp.CallToolResult, SellTaskOut, error) {
	return s.sellAction(ctx, req, in.ID, func(op SellOperator) error { return op.MuteBuyer(ctx, in.ID, in.Buyer, true) })
}

func (s *Server) unmuteSellTaskBuyer(ctx context.Context, req *mcp.CallToolRequest, in MuteBuyerIn) (*mcp.CallToolResult, SellTaskOut, error) {
	return s.sellAction(ctx, req, in.ID, func(op SellOperator) error { return op.MuteBuyer(ctx, in.ID, in.Buyer, false) })
}

// StartFriendSale saves a friend's meal and starts a sell task for it as
// userID. m.Slot names the meal ("dinner"); the meal gets its own slot so
// it never touches your own registration. Nothing stays saved when the
// task can't start. It returns the task id and the slot.
func (s *Server) StartFriendSale(ctx context.Context, userID int64, m store.FriendMeal, minRupees, maxRupees int) (int64, string, error) {
	sell, err := s.SellControl()
	if err != nil {
		return 0, "", err
	}
	if err := s.checkAccount(userID); err != nil {
		return 0, "", err
	}
	m.CreatedAt = s.now()
	slot, err := s.st.AddFriendMeal(ctx, m)
	if err != nil {
		return 0, "", err
	}
	id, err := sell.Start(ctx, userID, m.Date, slot, minRupees, maxRupees, "")
	if err != nil {
		if derr := s.st.DeleteFriendMeal(context.WithoutCancel(ctx), m.Date, slot); derr != nil {
			err = errors.Join(err, derr)
		}
		return 0, "", err
	}
	return id, slot, nil
}

// FriendMeals lists friends' meals from the date from, without QR images.
func (s *Server) FriendMeals(ctx context.Context, from string) ([]store.FriendMeal, error) {
	return s.st.FriendMeals(ctx, from)
}
