package config

import "fmt"

// Sell holds the settings for selling your own meals.
type Sell struct {
	// Handoff is the point where the bot stops and you take over the
	// buyer's chat. Empty means HandoffNever.
	Handoff Handoff `toml:"handoff"`
}

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
