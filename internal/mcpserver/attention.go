package mcpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"messwa/internal/store"
	"time"
)

// AttentionItem is one problem the seller has to act on. Kind is one of
// delivery, refund, blocked, schedule, post or whatsapp, so the dashboard
// can tag and colour it without matching on Title.
type AttentionItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Date       string `json:"date,omitempty"`
	Meal       string `json:"meal,omitempty"`
	Task       int64  `json:"task,omitempty"`
	Account    bool   `json:"account,omitempty"`
	Resolvable bool   `json:"resolvable"`
}

func (s *Server) Attention(ctx context.Context, user int64) ([]AttentionItem, error) {
	resolved, err := s.st.AttentionResolved(ctx, user)
	if err != nil {
		return nil, err
	}
	out := []AttentionItem{}
	add := func(kind, key, title, detail, date, meal string, task int64, account, resolvable bool) {
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(key+"|"+detail)))
		if !resolved[id] {
			out = append(out, AttentionItem{id, kind, title, detail, date, meal, task, account, resolvable})
		}
	}
	ds, err := s.Deliveries(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if d.State == "sending" {
			add("delivery", "delivery:"+d.Date+d.Meal, "Check QR delivery", "They paid, but the QR may not have reached them. Ask before you resend it.", d.Date, d.Meal, d.TaskID, false, true)
		}
	}
	tasks, err := s.st.SellTasks(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	active := false
	for _, t := range tasks {
		if t.State == store.TaskActive {
			active = true
		}
		bs, err := s.st.SellTaskBuyers(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if rupeesStr(b.Refund) > 0 {
				add("refund", fmt.Sprintf("refund:%d:%s", t.ID, b.Peer), "Refund due", fmt.Sprintf("Send ₹%s back to %s, unless they got the meal.", b.Refund, s.attentionWho(ctx, b)), t.Date, t.Meal, t.ID, false, true)
			}
		}
		if t.State == store.TaskFailed {
			add("blocked", fmt.Sprintf("task:%d", t.ID), "Sale blocked", t.Detail, t.Date, t.Meal, t.ID, false, true)
		}
		if t.State == store.TaskActive {
			posts, err := s.st.SellTaskOffers(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			latest := map[string]store.SellOffer{}
			for _, p := range posts {
				if old, ok := latest[p.Group]; !ok || p.Seq > old.Seq {
					latest[p.Group] = p
				}
			}
			for group, p := range latest {
				if p.State == store.OfferFailed {
					add("post", fmt.Sprintf("post:%d:%s:%d", t.ID, group, p.Seq), "Offer didn't post", p.Error, t.Date, t.Meal, t.ID, false, false)
				}
			}
		}
	}
	schedules, err := s.st.SellSchedules(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	for _, sc := range schedules {
		if sc.State == store.ScheduleWaiting {
			active = true
		}
		if sc.State == store.ScheduleFailed {
			add("schedule", fmt.Sprintf("schedule:%d", sc.ID), "Schedule failed", sc.Detail, sc.Date, sc.Meal, 0, false, true)
		}
	}
	if active && s.Device != nil {
		if status, ok := s.Device.Status().(map[string]any); ok && status["connected"] == false {
			add("whatsapp", "whatsapp", "WhatsApp disconnected", "Your sales can't post or reply until you reconnect.", "", "", 0, true, false)
		}
	}
	return out, nil
}

// attentionWho names a buyer the way the sale sheet does: the name you gave
// them, then their contact or push name, then their number.
func (s *Server) attentionWho(ctx context.Context, b store.SellBuyer) string {
	if b.Name != "" {
		return b.Name
	}
	p := s.person(ctx, b.Peer, b.Alt, "")
	if p.Name != "" {
		return p.Name
	}
	if p.Phone != "" {
		return p.Phone
	}
	return b.Peer
}

func (s *Server) ResolveAttention(ctx context.Context, user int64, id string) error {
	return s.st.ResolveAttention(ctx, user, id)
}
func (s *Server) AttentionSale(ctx context.Context, id int64) (*SaleView, error) {
	t, err := s.st.GetSellTask(ctx, id)
	if err != nil || t == nil {
		return nil, err
	}
	v, err := s.saleView(ctx, *t, s.now())
	return &v, err
}
