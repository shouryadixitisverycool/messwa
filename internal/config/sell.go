package config

import (
	"fmt"
	"time"
)

// Sell holds the settings for selling your own meals: the group offers that
// deliver-qr and sell tasks post, and when the bot hands a buyer to you. CLI
// flags override them for one deliver-qr run.
type Sell struct {
	// Groups are the groups an offer goes to, by exact name or JID. Leave
	// it empty to use GroupCount or pass -group on the command line.
	Groups []string `toml:"groups"`
	// GroupCount posts to the first N of the top-level groups instead of
	// naming them. Set Groups or GroupCount, not both.
	GroupCount int `toml:"group_count"`
	// PostGap is the pause between copies of one offer in different groups.
	PostGap duration `toml:"post_gap"`
	// ShowPrice puts the price in the group post. The payment request DM
	// states the price either way.
	ShowPrice bool `toml:"show_price"`
	// MentionExtras lists the extras registered for that meal in the post.
	MentionExtras bool `toml:"mention_extras"`
	// Handoff is the point where the bot stops and you take over the
	// buyer's chat. Empty means HandoffNever.
	Handoff Handoff `toml:"handoff"`
	// AcceptPlainDMs lets a DM that doesn't quote the offer post join the
	// buyer queue, if it asks about the offer and you weren't already
	// chatting with the sender. Off by default: with it on, the bot answers
	// people who never saw the post.
	AcceptPlainDMs bool `toml:"accept_plain_dms"`
}

// MinPostGap is the shortest pause allowed between copies of one offer.
const MinPostGap = 3 * time.Second

// Handoff says when a group sale stops being automatic.
type Handoff string

const (
	// HandoffNever lets the bot run the whole sale: it asks the first buyer
	// to pay and sends the QR when the payment arrives.
	HandoffNever Handoff = "never"
	// HandoffOnReply stops when a buyer replies privately to the offer,
	// before the bot sends them anything, so you can talk to them yourself.
	HandoffOnReply Handoff = "on_reply"
)

// ParseHandoff reads a handoff setting. Empty means HandoffNever.
func ParseHandoff(s string) (Handoff, error) {
	switch h := Handoff(s); h {
	case "", HandoffNever:
		return HandoffNever, nil
	case HandoffOnReply:
		return h, nil
	}
	return "", fmt.Errorf("handoff %q: use %q or %q", s, HandoffNever, HandoffOnReply)
}

func (h *Handoff) UnmarshalText(b []byte) error {
	v, err := ParseHandoff(string(b))
	*h = v
	return err
}

// HandsOffOnReply reports whether a buyer's private reply to an offer ends
// the bot's part of the sale. The bot then sends that buyer nothing.
func (h Handoff) HandsOffOnReply() bool { return h == HandoffOnReply }

// Resolve applies a per-run override, such as a -handoff flag, over the
// configured setting. An empty override keeps the setting.
func (h Handoff) Resolve(override string) (Handoff, error) {
	if override == "" {
		return ParseHandoff(string(h))
	}
	return ParseHandoff(override)
}
