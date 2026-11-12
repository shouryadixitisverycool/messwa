package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/portal"
	"messwa/internal/store"
	"messwa/internal/wa"
)

// soldReaction is what the seller reacts with on their own post once the meal
// has sold. The default [sold] config counts it.
const soldReaction = "✅"

// postedOffer is one group post that offered a meal. One offer can go out
// as several copies, one per group.
type postedOffer struct {
	Chat      types.JID
	GroupName string
	ID        string
	At        time.Time
	Text      string
}

// sendReaction reacts to one of the linked account's own group posts and
// returns the reaction's message ID and server time.
type sendReaction func(ctx context.Context, chat types.JID, target, emoji string) (string, time.Time, error)

func waReaction(c *wa.Client) sendReaction {
	return func(ctx context.Context, chat types.JID, target, emoji string) (string, time.Time, error) {
		// An empty sender marks the target as our own message.
		resp, err := c.WA.SendMessage(ctx, chat, c.WA.BuildReaction(chat, types.EmptyJID, target, emoji))
		return resp.ID, resp.Timestamp, err
	}
}

// ownGroupLog writes messwa's own group messages into the group log.
// WhatsApp doesn't send a message back to the device that sent it, so run
// never gets an event for an offer or ✅ that messwa sent itself. Without
// this the board would miss both. Posts from the phone arrive as normal
// IsFromMe events and don't need it.
type ownGroupLog struct {
	in             *ingest.Ingester
	jid, alt, name string
}

func newOwnGroupLog(ctx context.Context, cfg *config.Config, st *store.Store, c *wa.Client) ownGroupLog {
	jid, alt, name := c.Self(ctx)
	return ownGroupLog{in: ingest.New(cfg, st), jid: jid, alt: alt, name: name}
}

// add skips groups that aren't configured market groups, like a test group.
func (l ownGroupLog) add(ctx context.Context, e store.LogEntry) error {
	if l.in == nil || l.jid == "" {
		return nil
	}
	e.Sender, e.SenderAlt, e.PushName = l.jid, l.alt, l.name
	return l.in.Handle(ctx, e)
}

// recordOffers saves the posts in the delivery ledger, so the sale can tick
// them later, and adds them to the group log. Failures are logged: the offer
// is already out, and the worst case is a missing ✅.
func recordOffers(ctx context.Context, st *store.Store, own ownGroupLog, log *slog.Logger, date, meal string, posts []postedOffer) {
	for _, p := range posts {
		if err := st.RecordOfferPost(ctx, store.OfferPost{Date: date, Meal: meal, Chat: p.Chat.String(), ID: p.ID, PostedAt: p.At}); err != nil {
			log.Warn("could not record the offer post; mark it sold by hand later", "group", p.Chat.String(), "offer_id", p.ID, "err", err)
		}
		if err := own.add(ctx, store.LogEntry{Chat: p.Chat.String(), ID: p.ID, Type: "post", TS: p.At, Text: p.Text}); err != nil {
			log.Warn("could not add the offer post to the group log", "offer_id", p.ID, "err", err)
		}
	}
}

// reactSold reacts ✅ to every recorded offer post for the meal and adds each
// reaction to the group log, so the board shows the listing as sold. A failed
// reaction is logged and skipped. It returns how many posts got the reaction
// out of how many are recorded; err is set only when the posts can't be read.
func reactSold(ctx context.Context, st *store.Store, own ownGroupLog, react sendReaction, log *slog.Logger, date, meal string) (done, total int, err error) {
	posts, err := st.OfferPosts(ctx, date, meal)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range posts {
		chat, err := types.ParseJID(p.Chat)
		if err != nil {
			log.Warn("recorded offer post has a bad group JID", "group", p.Chat, "offer_id", p.ID)
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		id, at, err := react(sendCtx, chat, p.ID, soldReaction)
		cancel()
		if err != nil {
			log.Warn("could not react to the offer post; the sale still stands", "group", p.Chat, "offer_id", p.ID, "err", err)
			continue
		}
		done++
		at = firstTime(at, time.Now())
		if err := st.MarkOfferReacted(ctx, p.Chat, p.ID, at); err != nil {
			log.Warn("could not record the reaction", "offer_id", p.ID, "err", err)
		}
		if err := own.add(ctx, store.LogEntry{Chat: p.Chat, ID: id, Type: "reaction", TS: at, Text: soldReaction, Target: p.ID}); err != nil {
			log.Warn("could not add the reaction to the group log", "offer_id", p.ID, "err", err)
		}
		log.Info("reacted to the offer post", "group", p.Chat, "offer_id", p.ID, "reaction", soldReaction)
	}
	return done, len(posts), nil
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// markSoldArgs is a parsed mark-sold command line.
type markSoldArgs struct {
	sale  store.ManualSale
	user  string
	force bool
	react bool
}

// parseMarkSold reads mark-sold's flags. now is the current time.
func parseMarkSold(cfg *config.Config, args []string, now time.Time) (markSoldArgs, error) {
	fs := flag.NewFlagSet("mark-sold", flag.ContinueOnError)
	date := fs.String("date", "", "meal date, YYYY-MM-DD in the configured timezone")
	meal := fs.String("meal", "", "configured meal name")
	peer := fs.String("peer", "", "the buyer: phone number with country code, or WhatsApp JID")
	name := fs.String("name", "", "the buyer's name, for the sales page")
	amount := fs.String("amount", "", "price paid in INR, e.g. 20 or 20.00")
	paidBy := fs.String("paid-by", "whatsapp", "how the buyer paid: whatsapp, image (a screenshot, unverified) or other")
	at := fs.String("at", "", "when the QR went to the buyer: HH:MM on -date, or RFC 3339 (default now)")
	user := fs.String("user", "", "whose mess API key reads the registration's mess and diet; defaults to the first admin")
	force := fs.Bool("force", false, "overwrite a sale already recorded for the meal")
	react := fs.Bool("react", true, "react ✅ to the meal's recorded offer posts (needs the WhatsApp session, so stop run first)")
	if err := fs.Parse(args); err != nil {
		return markSoldArgs{}, err
	}
	if fs.NArg() != 0 {
		return markSoldArgs{}, errors.New("unexpected positional arguments")
	}
	day, err := time.ParseInLocation("2006-01-02", *date, cfg.Location())
	if err != nil {
		return markSoldArgs{}, errors.New("-date must be YYYY-MM-DD")
	}
	known := false
	for _, m := range cfg.Meals {
		known = known || m.Name == *meal
	}
	if !known {
		return markSoldArgs{}, errors.New("-meal must name a configured meal")
	}
	buyer, err := parseBuyerJID(*peer)
	if err != nil {
		return markSoldArgs{}, err
	}
	paise, err := store.ParsePaise(wholeRupees(*amount))
	if err != nil || paise <= 0 {
		return markSoldArgs{}, errors.New("-amount must be a positive INR amount, like 20 or 20.50")
	}
	kind, ok := map[string]string{"whatsapp": "", "image": store.PaidByImage, "other": store.PaidOther}[*paidBy]
	if !ok {
		return markSoldArgs{}, errors.New("-paid-by must be whatsapp, image or other")
	}
	when := now
	if *at != "" {
		if when, err = parseSaleTime(*at, day, cfg.Location()); err != nil {
			return markSoldArgs{}, err
		}
		if when.After(now.Add(time.Minute)) {
			return markSoldArgs{}, errors.New("-at is in the future")
		}
	}
	return markSoldArgs{
		sale: store.ManualSale{Date: *date, Meal: *meal, Peer: buyer.String(), Amount: store.FormatPaise(paise),
			BuyerName: strings.TrimSpace(*name), Kind: kind, At: when.Truncate(time.Second)},
		user: *user, force: *force, react: *react,
	}, nil
}

// wholeRupees adds ".00" to an amount without paise.
func wholeRupees(amount string) string {
	amount = strings.TrimPrefix(strings.TrimSpace(amount), "₹")
	if amount != "" && !strings.Contains(amount, ".") {
		amount += ".00"
	}
	return amount
}

// parseSaleTime reads -at: a clock time on day, or a full RFC 3339 time.
func parseSaleTime(s string, day time.Time, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	clock, err := time.ParseInLocation("15:04", s, loc)
	if err != nil {
		return time.Time{}, errors.New("-at must be HH:MM or an RFC 3339 time")
	}
	return time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, loc), nil
}

// parseBuyerJID reads a buyer as a phone number with its country code
// ("+91 98765 43210") or a personal WhatsApp JID.
func parseBuyerJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.EmptyJID, errors.New("-peer is required: the buyer's phone number or WhatsApp JID")
	}
	if strings.Contains(s, "@") {
		jid, err := types.ParseJID(s)
		if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
			return types.EmptyJID, errors.New("-peer must be a personal WhatsApp JID or a phone number")
		}
		return jid.ToNonAD(), nil
	}
	digits := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
			return -1
		}
		return 'x'
	}, s)
	switch {
	case strings.Contains(digits, "x"):
		return types.EmptyJID, errors.New("-peer must be a personal WhatsApp JID or a phone number")
	case len(digits) <= 10:
		return types.EmptyJID, errors.New("-peer needs the country code, like +91 98765 43210")
	case len(digits) > 15:
		return types.EmptyJID, errors.New("-peer is too long for a phone number")
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

// registrationReader is the part of portal.Client mark-sold uses.
type registrationReader interface {
	Registrations(ctx context.Context, from, to string) ([]portal.Registration, error)
}

// saleMessDiet reads the meal's mess and diet from its registration. The
// meal may be eaten by now, so availed registrations count.
func saleMessDiet(ctx context.Context, cfg *config.Config, p registrationReader, date, meal string) (string, string, error) {
	regs, err := p.Registrations(ctx, date, date)
	if err != nil {
		return "", "", err
	}
	for _, r := range regs {
		if r.Date == date && r.Meal == meal && !r.Cancelled() {
			mess, diet := r.MessDiet()
			if diet == "" && cfg.VegOnly(mess) {
				diet = "veg"
			}
			return mess, diet, nil
		}
	}
	return "", "", errors.New("no registration for that meal")
}

// recordSale writes a mark-sold sale to the ledger and says what it did.
// p may be nil; without the registration the row keeps the mess and diet
// it has.
func recordSale(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger, p registrationReader, a markSoldArgs) (string, error) {
	m := a.sale
	if p != nil {
		var err error
		if m.Mess, m.Diet, err = saleMessDiet(ctx, cfg, p, m.Date, m.Meal); err != nil {
			log.Warn("could not read the meal's registration; recording the sale without its mess and diet", "err", err)
		}
	}
	prev, err := st.RecordManualSale(ctx, m, a.force)
	if err != nil {
		return "", err
	}
	how := map[string]string{"": "WhatsApp Pay", store.PaidByImage: "image, unverified", store.PaidOther: "another way"}[m.Kind]
	msg := fmt.Sprintf("Recorded %s %s as sold to %s for %s (paid by %s) at %s.", m.Date, m.Meal, m.Peer, rupees(m.Amount), how, m.At.In(cfg.Location()).Format("15:04"))
	if prev != "" {
		msg += fmt.Sprintf(" It was %s before.", prev)
	}
	return msg, nil
}

// markSold records a sale finished outside messwa's own delivery, for
// example after you took over the buyer's chat or sent the QR yourself,
// and ticks the meal's offer posts.
func markSold(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	a, err := parseMarkSold(cfg, args, time.Now())
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return err
	}
	defer st.Close()
	var p registrationReader
	if client, err := userPortal(ctx, cfg, a.user); err != nil {
		log.Warn("could not open the portal; recording the sale without its mess and diet", "err", err)
	} else {
		p = client
	}
	msg, err := recordSale(ctx, cfg, st, log, p, a)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	if !a.react {
		return nil
	}
	posts, err := st.OfferPosts(ctx, a.sale.Date, a.sale.Meal)
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		fmt.Printf("No offer posts recorded for %s %s, so nothing to react to. Posts made from your phone aren't recorded; react to those there.\n", a.sale.Date, a.sale.Meal)
		return nil
	}
	c, err := wa.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	if !c.LoggedIn() {
		return errors.New("not linked; run messwa login first")
	}
	if err := c.WA.ConnectContext(ctx); err != nil {
		return err
	}
	defer c.WA.Disconnect()
	if !c.WA.WaitForConnection(20 * time.Second) {
		return errors.New("WhatsApp connection not ready")
	}
	done, total, err := reactSold(ctx, st, newOwnGroupLog(ctx, cfg, st, c), waReaction(c), log, a.sale.Date, a.sale.Meal)
	if err != nil {
		return err
	}
	if done < total {
		return fmt.Errorf("reacted %s to %d of %d offer posts; see the warnings above", soldReaction, done, total)
	}
	fmt.Printf("Reacted %s to %d offer post(s) for %s %s.\n", soldReaction, done, a.sale.Date, a.sale.Meal)
	return nil
}
