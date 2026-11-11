package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/wa"
)

func offerGroup(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("offer-group", flag.ContinueOnError)
	group := fs.String("group", "", "exact group name or group JID")
	offer := fs.String("offer-text", "", "text to post in the group")
	reply := fs.String("reply-text", "", "first private response to a quoted offer")
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for the first private reply")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*group) == "" || strings.TrimSpace(*offer) == "" || strings.TrimSpace(*reply) == "" || *wait <= 0 {
		return errors.New("provide -group, -offer-text and -reply-text, with a positive wait time")
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
	peer, err := postGroupOffer(ctx, c, log, *group, *offer, *wait)
	if err != nil {
		return err
	}
	if _, err := c.WA.SendMessage(ctx, peer, &waE2E.Message{Conversation: proto.String(*reply)}); err != nil {
		return fmt.Errorf("private reply send failed or is uncertain: %w", err)
	}
	log.Info("matched private offer reply and sent the first response")
	return nil
}

// postGroupOffer uses the caller's connection so it can continue the sale in
// the buyer's DM as soon as the first matching reply arrives.
func postGroupOffer(ctx context.Context, c *wa.Client, log *slog.Logger, group, offer string, wait time.Duration) (types.JID, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	queue, closeOffer, err := openGroupOffer(ctx, c, log, group, offer)
	if err != nil {
		return types.EmptyJID, err
	}
	defer closeOffer()
	return queue.next(ctx)
}

type offerQueue struct {
	// posts are the group posts this offer went out as.
	posts  []postedOffer
	mu     sync.Mutex
	buyers []types.JID
	seen   map[types.JID]bool
	ready  chan struct{}
	closed bool
}

func (q *offerQueue) add(peer types.JID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.seen[peer] {
		return
	}
	q.seen[peer] = true
	q.buyers = append(q.buyers, peer)
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *offerQueue) closeAndDrain() []types.JID {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	waiting := q.buyers
	q.buyers = nil
	return waiting
}

func (q *offerQueue) next(ctx context.Context) (types.JID, error) {
	for ctx.Err() == nil {
		q.mu.Lock()
		if len(q.buyers) > 0 {
			peer := q.buyers[0]
			q.buyers = q.buyers[1:]
			q.mu.Unlock()
			return peer, nil
		}
		q.mu.Unlock()
		select {
		case <-q.ready:
		case <-ctx.Done():
		}
	}
	return types.EmptyJID, fmt.Errorf("waiting for a private reply: %w", ctx.Err())
}

func openGroupOffer(ctx context.Context, c *wa.Client, log *slog.Logger, group, offer string) (*offerQueue, func(), error) {
	groups, err := c.Groups(ctx)
	if err != nil {
		return nil, nil, err
	}
	var selected *wa.GroupSummary
	for i := range groups {
		g := &groups[i]
		if g.JID != group && (strings.HasSuffix(group, "@g.us") || g.Name != group) {
			continue
		}
		if selected != nil {
			return nil, nil, errors.New("group name is ambiguous; use its exact JID")
		}
		selected = g
	}
	if selected == nil {
		return nil, nil, errors.New("no joined group matches -group")
	}
	jid, err := types.ParseJID(selected.JID)
	if err != nil || jid.Server != types.GroupServer || jid.User == "" {
		return nil, nil, errors.New("selected group has an invalid JID")
	}

	offerID := c.WA.GenerateMessageID()
	postedAt := time.Now().Truncate(time.Second)
	queue := &offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
	handlerID := c.WA.AddEventHandler(func(evt any) {
		e, ok := evt.(*events.Message)
		if !ok || !privateOfferReply(e, jid, offerID, postedAt) || buyerPassed(e.Message) {
			return
		}
		peer := e.Info.Chat.ToNonAD()
		if alt := e.Info.SenderAlt.ToNonAD(); alt.User != "" && alt.Server == types.DefaultUserServer {
			peer = alt
		} else if peer.Server == types.HiddenUserServer {
			if pn, err := c.WA.Store.LIDs.GetPNForLID(ctx, peer); err == nil && pn.User != "" {
				peer = pn.ToNonAD()
			}
		}
		queue.add(peer)
	})
	closeOffer := func() { c.WA.RemoveEventHandler(handlerID) }
	resp, err := c.WA.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(offer)}, whatsmeow.SendRequestExtra{ID: offerID})
	if err != nil {
		closeOffer()
		return nil, nil, fmt.Errorf("group offer send failed or is uncertain: %w", err)
	}
	queue.posts = append(queue.posts, postedOffer{Chat: jid, ID: offerID, At: firstTime(resp.Timestamp, postedAt), Text: offer})
	log.Info("group offer sent; waiting for a private reply", "group", selected.Name, "offer_id", offerID)
	return queue, closeOffer, nil
}

func privateOfferReply(e *events.Message, group types.JID, offerID string, postedAt time.Time) bool {
	if e == nil || e.Info.IsFromMe || e.Info.IsGroup || e.Info.Timestamp.Before(postedAt) || e.Info.Chat.User == "" {
		return false
	}
	if e.Info.Chat.Server != types.DefaultUserServer && e.Info.Chat.Server != types.HiddenUserServer {
		return false
	}
	msg := e.Message.GetExtendedTextMessage()
	quote := msg.GetContextInfo()
	if strings.TrimSpace(msg.GetText()) == "" || offerID == "" || quote.GetStanzaID() != offerID {
		return false
	}
	return quote.GetRemoteJID() == "" || quote.GetRemoteJID() == group.String()
}
