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
	"price", "prize", "rate", "cost", "how much", "hw much", "hw mch", "hwmuch", "how mcuh", "kitna", "kitne", "kitni",
	"available", "availabe", "availble", "avail", "avl", "still there", "selling", "sold",
	"interested", "intrested", "buy", "buying", "take it",
}

// declineWords turn a DM down, like "not interested in buying".
var declineWords = []string{"not interested", "no thanks", "dont want", "don t want", "do not want", "donot want", "dont need", "don t need", "do not need", "donot need", "nahi chahiye"}

// offerTopic is what an offer sells, for telling whether a DM is about it.
type offerTopic struct {
	// mess is empty when the offer doesn't say, e.g. a hand-written
	// -offer-text that names no mess.
	mess, diet, meal, extra string
	messes, meals           []classify.Named
}

// newOfferTopic describes an offer of meal at mess. With mess unknown it
// takes the first mess the offer text names.
func newOfferTopic(cfg *config.Config, mess, meal, offer string) offerTopic {
	return newOfferTopicForExtra(cfg, mess, "", meal, "", offer)
}

func newOfferTopicForExtra(cfg *config.Config, mess, diet, meal, extra, offer string) offerTopic {
	t := offerTopic{mess: mess, diet: diet, meal: meal, extra: strings.ToLower(strings.TrimSpace(extra))}
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

// extraSkipWords are generic size/portion modifiers that shouldn't match on
// their own in a DM or buy post (e.g. "Omelette Full" should match "omelette",
// not "full").
var extraSkipWords = map[string]bool{
	"full": true, "half": true, "plate": true, "piece": true, "pieces": true, "pcs": true, "extra": true, "extras": true, "special": true, "with": true, "and": true,
}

// extraKeywords returns words/phrases that identify a separately sold extra in
// a DM or buy post (e.g. "Egg" -> ["egg", "eggs"], "Omelette Full" ->
// ["omelette full", "omelette", "omelettes", "omlette", "egg", "eggs"]).
func extraKeywords(extra string) []string {
	norm := classify.Normalize(extra)
	if norm == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		if w != "" && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	add(norm)
	if !strings.Contains(norm, " ") && !strings.HasSuffix(norm, "s") {
		add(norm + "s")
	}
	words := strings.Fields(norm)
	isEggItem := false
	for _, w := range words {
		if w == "egg" || w == "eggs" || w == "omelette" || w == "omlette" || w == "omelet" {
			isEggItem = true
		}
		if len(w) < 3 || extraSkipWords[w] {
			continue
		}
		add(w)
		if !strings.HasSuffix(w, "s") {
			add(w + "s")
		}
	}
	if isEggItem {
		for _, alias := range []string{"egg", "eggs", "omelette", "omelettes", "omlette", "omelet"} {
			add(alias)
		}
	}
	return out
}

// extraNegated reports whether text explicitly excludes any of keywords, e.g.
// "buy kadamba dinner no eggs" or "without egg".
func extraNegated(t classify.Text, keywords []string) bool {
	for _, kw := range keywords {
		if t.Has("no "+kw, "without "+kw, "wo "+kw, "w o "+kw) {
			return true
		}
	}
	return false
}

// isExtraOnlyText reports whether text explicitly asks for ONLY an extra item
// (not the full meal), e.g. "Can I buy only the egg for Kadamba dinner?".
func isExtraOnlyText(t classify.Text) bool {
	return t.Has(
		"extra only", "extras only",
		"only egg", "only eggs", "only the egg", "only the eggs",
		"egg only", "eggs only",
		"just egg", "just eggs", "just the egg", "just the eggs",
		"only omelette", "only omelettes", "only the omelette", "only the omelettes",
		"omelette only", "just omelette", "just the omelette",
	)
}

// asksAbout reports whether text asks about this offer. A DM that names
// only other messes or other meals is about something else. For a separately
// sold extra, the DM must explicitly mention that extra (without negating it
// or asking for a different meal diet) and show buying intent so casual chats
// mentioning "egg" don't trigger quotes. Conversely, a regular veg/nonveg meal
// ignores bare egg asks that don't name the meal (e.g. "egg available?"), while
// still accepting meal asks like "egg dinner available?".
func (o offerTopic) asksAbout(text string) bool {
	t := classify.Prepare(text)
	if t.Has(declineWords...) {
		return false
	}
	mealOurs, mealOther := splitHits(classify.Pick(t, o.meals), o.meal)
	if mealOther && !mealOurs {
		return false
	}
	if o.extra == "" && (isExtraOnlyText(t) || (o.diet != "" && o.diet != "egg" && !mealOurs && classify.Diet(t) == "egg")) {
		return false
	}
	var messOurs bool
	if o.mess != "" {
		var messOther bool
		messOurs, messOther = splitHits(classify.Pick(t, o.messes), o.mess)
		if messOther && !messOurs {
			return false
		}
	}
	if o.extra != "" {
		kws := extraKeywords(o.extra)
		if !t.Has(kws...) || extraNegated(t, kws) {
			return false
		}
		if d := classify.Diet(t); d != "" && d != "egg" {
			return false
		}
		if t.Has(askWords...) && (messOurs || mealOurs || t.Words() <= shortAsk) {
			return true
		}
		return (messOurs && t.Words() <= 3) || t.Words() <= 2
	}
	if messOurs {
		return true
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
	return newPlainDMsForExtra(cfg, st, log, mess, "", meal, "", offer)
}

func newPlainDMsForExtra(cfg *config.Config, st *store.Store, log *slog.Logger, mess, diet, meal, extra, offer string) *plainDMs {
	return &plainDMs{
		topic: newOfferTopicForExtra(cfg, mess, diet, meal, extra, offer),
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
