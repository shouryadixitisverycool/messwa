package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/store"
	"messwa/internal/wa"
)

// soldReaction is what the seller reacts with on their own post once the meal
// has sold. The default [sold] config counts it.
const soldReaction = "✅"

// postedOffer is one group post that offered a meal.
type postedOffer struct {
	Chat types.JID
	ID   string
	At   time.Time
	Text string
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

// markSold ticks a meal's offer posts for a sale finished outside messwa,
// for example after you took over the buyer's chat.
func markSold(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("mark-sold", flag.ContinueOnError)
	date := fs.String("date", "", "meal date, YYYY-MM-DD in the configured timezone")
	meal := fs.String("meal", "", "configured meal name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if _, err := time.ParseInLocation("2006-01-02", *date, cfg.Location()); err != nil {
		return errors.New("-date must be YYYY-MM-DD")
	}
	known := false
	for _, m := range cfg.Meals {
		known = known || m.Name == *meal
	}
	if !known {
		return errors.New("-meal must name a configured meal")
	}
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return err
	}
	defer st.Close()
	posts, err := st.OfferPosts(ctx, *date, *meal)
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		return fmt.Errorf("no offer posts recorded for %s %s; only posts made with deliver-qr -group are recorded, so react to others from your phone", *date, *meal)
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
	done, total, err := reactSold(ctx, st, newOwnGroupLog(ctx, cfg, st, c), waReaction(c), log, *date, *meal)
	if err != nil {
		return err
	}
	if done < total {
		return fmt.Errorf("reacted %s to %d of %d offer posts; see the warnings above", soldReaction, done, total)
	}
	fmt.Printf("Reacted %s to %d offer post(s) for %s %s.\n", soldReaction, done, *date, *meal)
	return nil
}
