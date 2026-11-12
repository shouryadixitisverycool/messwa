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
	// Buyers is who may buy from an offer. Empty means BuyersAnyone.
	Buyers Buyers `toml:"buyers"`
	// Repost puts a fresh copy of the offer in a group once the last one
	// has dropped out of view, while the sale is still open. The copy goes
	// out when the first of RepostAfter, RepostMessages or RepostListings
	// is reached in that group.
	Repost bool `toml:"repost"`
	// RepostAfter is the longest a copy stays up before a fresh one goes
	// out. 0 turns this trigger off.
	RepostAfter duration `toml:"repost_after"`
	// RepostMessages reposts once this many messages came in after the
	// copy, enough to push it off a phone screen. 0 turns it off.
	RepostMessages int `toml:"repost_messages"`
	// RepostListings reposts once this many other people listed the same
	// meal at the same mess after the copy. 0 turns it off.
	RepostListings int `toml:"repost_listings"`
	// MaxReposts caps fresh copies per group for one offer.
	MaxReposts int `toml:"max_reposts"`
	// AcceptPaymentImages counts an image from the buyer whose payment
	// turn it is as their payment, once they have the payment prompt. Off
	// by default: a screenshot proves nothing, and a QR can't be taken
	// back once sent.
	AcceptPaymentImages bool `toml:"accept_payment_images"`
}

// MinRepostGap is the shortest time between two copies in one group,
// whatever the message and listing counts say. repost_after can't be
// shorter.
const MinRepostGap = 5 * time.Minute

// RepostCutoff is how long before the meal ends the last repost may go
// out. Few meals sell later than this, and a buyer who replies then has
// little time to haggle and pay.
const RepostCutoff = 15 * time.Minute

// MaxRepostsLimit is the most max_reposts may be.
const MaxRepostsLimit = 12

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
	// HandoffOnAgreement lets a sell task negotiate, then hands you the chat
	// at the agreed price instead of asking the buyer to pay. Only sell
	// tasks negotiate, so deliver-qr and [sell] don't take it.
	HandoffOnAgreement Handoff = "on_agreement"
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

// ParseTaskHandoff reads a sell task's handoff, which may also be
// on_agreement. Empty means HandoffNever.
func ParseTaskHandoff(s string) (Handoff, error) {
	if Handoff(s) == HandoffOnAgreement {
		return HandoffOnAgreement, nil
	}
	h, err := ParseHandoff(s)
	if err != nil {
		return "", fmt.Errorf("handoff %q: use %q, %q or %q", s, HandoffNever, HandoffOnReply, HandoffOnAgreement)
	}
	return h, nil
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

// Buyers says who a group offer sells to.
type Buyers string

const (
	// BuyersAnyone sells to anyone who replies privately to the offer.
	BuyersAnyone Buyers = "anyone"
	// BuyersUnsavedOnly skips replies from saved contacts: the bot sends
	// them nothing and sells only to numbers not in your address book.
	BuyersUnsavedOnly Buyers = "unsaved_only"
)

// ParseBuyers reads a buyers setting. Empty means BuyersAnyone.
func ParseBuyers(s string) (Buyers, error) {
	switch b := Buyers(s); b {
	case "", BuyersAnyone:
		return BuyersAnyone, nil
	case BuyersUnsavedOnly:
		return b, nil
	}
	return "", fmt.Errorf("buyers %q: use %q or %q", s, BuyersAnyone, BuyersUnsavedOnly)
}

func (b *Buyers) UnmarshalText(text []byte) error {
	v, err := ParseBuyers(string(text))
	*b = v
	return err
}

// UnsavedOnly reports whether saved contacts are turned away.
func (b Buyers) UnsavedOnly() bool { return b == BuyersUnsavedOnly }
