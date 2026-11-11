package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/classify"
	"messwa/internal/config"
	"messwa/internal/store"
)

// Plain DMs. With [sell] accept_plain_dms on, a DM that doesn't quote the
// offer post can still make its sender a buyer, when all of these hold:
//
//   - it is a new incoming text DM, not an edit, and quotes nothing;
//   - it was sent after the first copy of the offer was posted;
//   - its text asks about this offer (offerTopic.asksAbout);
//   - you sent that person no DM in the chatQuietFor before it, so a chat
//     you're in the middle of never gets a payment prompt from the bot.
//
// From there it is the quoted-reply path: groupOffer.buyer, the same queue
// and join record, and the same task gate on every send. The buyer gets the
// usual payment prompt, and only a matching payment releases the QR.

// chatQuietFor is how long you must not have messaged someone before their
// DM for it to count as a plain DM about the offer.
const chatQuietFor = 30 * time.Minute

// shortAsk is the most words a DM may have when it asks about the offer
// without naming the mess or the meal, like "price?" or "still available?".
const shortAsk = 8

// askWords are what buyers write when they DM about a post.
var askWords = []string{
	"price", "prize", "rate", "cost", "how much", "hw much", "kitna", "kitne", "kitni",
	"available", "availabe", "availble", "avail", "still there", "selling", "sold",
	"interested", "intrested", "buy", "buying", "take it",
}

// declineWords turn a DM down, like "not interested in buying".
var declineWords = []string{"not interested", "no thanks", "dont want", "don t want", "do not want", "nahi chahiye"}

// offerTopic is what an offer sells, for telling whether a DM is about it.
type offerTopic struct {
	// mess is empty when the offer doesn't say, e.g. a hand-written
	// -offer-text that names no mess.
	mess, meal    string
	messes, meals []classify.Named
}

// newOfferTopic describes an offer of meal at mess. With mess unknown it
// takes the first mess the offer text names.
func newOfferTopic(cfg *config.Config, mess, meal, offer string) offerTopic {
	t := offerTopic{mess: mess, meal: meal}
	for _, m := range cfg.Meals {
		t.meals = append(t.meals, classify.Named{Name: m.Name, Aliases: m.Keywords, Fuzzy: true})
	}
	for _, m := range cfg.Messes {
		t.messes = append(t.messes, classify.Named{Name: m.Name, Aliases: m.Aliases, Fuzzy: true})
	}
	if t.mess == "" {
		if hits := classify.Pick(classify.Prepare(offer), t.messes); len(hits) > 0 {
			t.mess = hits[0].Name
		}
	}
	return t
}

// asksAbout reports whether text asks about this offer. A DM that names
// only other messes or other meals is about something else. Otherwise it
// counts when it names the offer's mess, or asks a buying question (price,
// availability) and either names the meal or is at most shortAsk words.
func (o offerTopic) asksAbout(text string) bool {
	t := classify.Prepare(text)
	if t.Has(declineWords...) {
		return false
	}
	mealOurs, mealOther := splitHits(classify.Pick(t, o.meals), o.meal)
	if mealOther && !mealOurs {
		return false
	}
	if o.mess != "" {
		messOurs, messOther := splitHits(classify.Pick(t, o.messes), o.mess)
		if messOther && !messOurs {
			return false
		}
		if messOurs {
			return true
		}
	}
	return t.Has(askWords...) && (mealOurs || t.Words() <= shortAsk)
}

// splitHits reports whether hits include name, and whether they include
// anything else.
func splitHits(hits []classify.Hit, name string) (ours, other bool) {
	for _, h := range hits {
		if h.Name == name {
			ours = true
		} else {
			other = true
		}
	}
	return ours, other
}

// plainDMs is how a group offer judges DMs that don't quote it.
type plainDMs struct {
	topic offerTopic
	// lastSent is when you last sent a DM to any of ids, from the DM
	// records run keeps. Zero if never.
	lastSent func(ctx context.Context, ids ...types.JID) (time.Time, error)
	log      *slog.Logger
}

// newPlainDMs judges plain DMs against an offer of meal at mess, using st
// for when you last messaged each sender.
func newPlainDMs(cfg *config.Config, st *store.Store, log *slog.Logger, mess, meal, offer string) *plainDMs {
	return &plainDMs{
		topic: newOfferTopic(cfg, mess, meal, offer),
		lastSent: func(ctx context.Context, ids ...types.JID) (time.Time, error) {
			s := make([]string, len(ids))
			for i, id := range ids {
				s[i] = id.String()
			}
			return st.LastSentTo(ctx, s...)
		},
		log: log,
	}
}

// dmText is the text of a plain or extended text message.
func dmText(m *waE2E.Message) string {
	if text := m.GetConversation(); text != "" {
		return text
	}
	return m.GetExtendedTextMessage().GetText()
}

// personalChat reports whether e is a DM with a person, not a group,
// status or newsletter.
func personalChat(e *events.Message) bool {
	s := e.Info.Chat.Server
	return !e.Info.IsGroup && e.Info.Chat.User != "" && (s == types.DefaultUserServer || s == types.HiddenUserServer)
}

// plainCandidate reports whether e is a DM about the offer that doesn't
// quote it, before checking whether you're already chatting with them.
// Call it with o.mu held.
func (o *groupOffer) plainCandidate(e *events.Message) bool {
	if o.plain == nil || e.Info.IsFromMe || e.IsEdit || !personalChat(e) || len(o.copies) == 0 {
		return false
	}
	first := o.copies[0].At
	for _, p := range o.copies[1:] {
		if p.At.Before(first) {
			first = p.At
		}
	}
	if e.Info.Timestamp.Before(first) {
		return false
	}
	// A quote of anything but the offer belongs to another conversation.
	if e.Message.GetExtendedTextMessage().GetContextInfo().GetStanzaID() != "" {
		return false
	}
	text := dmText(e.Message)
	return strings.TrimSpace(text) != "" && o.plain.topic.asksAbout(text)
}

// noteSent remembers when you messaged someone from your phone while the
// offer is up, since deliver-qr has no DM records of its own. Call it with
// o.mu held.
func (o *groupOffer) noteSent(e *events.Message) {
	if o.plain == nil || !e.Info.IsFromMe || !personalChat(e) {
		return
	}
	if o.sentDM == nil {
		o.sentDM = map[types.JID]time.Time{}
	}
	for _, id := range []types.JID{e.Info.Chat.ToNonAD(), e.Info.RecipientAlt.ToNonAD()} {
		if id.User != "" && e.Info.Timestamp.After(o.sentDM[id]) {
			o.sentDM[id] = e.Info.Timestamp
		}
	}
}

// chatting reports whether you sent peer a DM in the chatQuietFor before
// e, or since. A failed lookup counts as chatting.
func (o *groupOffer) chatting(ctx context.Context, e *events.Message, peer types.JID) bool {
	ids := []types.JID{e.Info.Chat.ToNonAD()}
	if peer != ids[0] {
		ids = append(ids, peer)
	}
	since := e.Info.Timestamp.Add(-chatQuietFor)
	o.mu.Lock()
	p := o.plain
	for _, id := range ids {
		if at, ok := o.sentDM[id]; ok && !at.Before(since) {
			o.mu.Unlock()
			return true
		}
	}
	o.mu.Unlock()
	last, err := p.lastSent(ctx, ids...)
	if err != nil {
		p.log.Warn("could not check when you last messaged a DM sender; ignoring their DM", "err", err)
		return true
	}
	return !last.IsZero() && !last.Before(since)
}
