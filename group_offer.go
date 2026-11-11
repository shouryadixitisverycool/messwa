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
	conn := waConn{c.WA}
	queue, closeOffer, err := openGroupOffer(ctx, conn, c.WA.SendMessage, log, group, offer)
	if err != nil {
		return types.EmptyJID, err
	}
	defer closeOffer()
	return queue.next(ctx)
}

type offerQueue struct {
	mu     sync.Mutex
	buyers []types.JID
	seen   map[types.JID]bool
	ready  chan struct{}
	closed bool
	// join, when set, records a new buyer before they are queued. A buyer
	// it refuses is dropped.
	join func(types.JID) bool
}

func newOfferQueue() *offerQueue {
	return &offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
}

func (q *offerQueue) add(peer types.JID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.seen[peer] {
		return
	}
	if q.join != nil && !q.join(peer) {
		return
	}
	q.seen[peer] = true
	q.buyers = append(q.buyers, peer)
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// restore puts back a buyer who was queued before a restart, without
// recording them again.
func (q *offerQueue) restore(peer types.JID, waiting bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seen[peer] = true
	if waiting {
		q.buyers = append(q.buyers, peer)
		select {
		case q.ready <- struct{}{}:
		default:
		}
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

// findGroup resolves an exact group name or JID among the joined groups.
func findGroup(ctx context.Context, conn saleConn, group string) (types.JID, string, error) {
	groups, err := conn.GetJoinedGroups(ctx)
	if err != nil {
		return types.EmptyJID, "", err
	}
	var selected *types.GroupInfo
	for _, g := range groups {
		if g.JID.String() != group && (strings.HasSuffix(group, "@g.us") || g.Name != group) {
			continue
		}
		if selected != nil {
			return types.EmptyJID, "", errors.New("group name is ambiguous; use its exact JID")
		}
		selected = g
	}
	if selected == nil {
		return types.EmptyJID, "", errors.New("no joined group matches the requested group")
	}
	if selected.JID.Server != types.GroupServer || selected.JID.User == "" {
		return types.EmptyJID, "", errors.New("selected group has an invalid JID")
	}
	return selected.JID, selected.Name, nil
}

// postedOffer is one group post that buyers reply to privately.
type postedOffer struct {
	group    types.JID
	id       string
	postedAt time.Time
}

// watchOfferReplies queues everyone who replies privately to any of offers,
// by phone number when WhatsApp tells us one.
func watchOfferReplies(ctx context.Context, conn saleConn, queue *offerQueue, offers []postedOffer) func() {
	handlerID := conn.AddEventHandler(func(evt any) {
		e, ok := evt.(*events.Message)
		if !ok || buyerPassed(e.Message) {
			return
		}
		matched := false
		for _, o := range offers {
			if privateOfferReply(e, o.group, o.id, o.postedAt) {
				matched = true
				break
			}
		}
		if !matched {
			return
		}
		peer := e.Info.Chat.ToNonAD()
		if alt := e.Info.SenderAlt.ToNonAD(); alt.User != "" && alt.Server == types.DefaultUserServer {
			peer = alt
		} else if peer.Server == types.HiddenUserServer {
			if pn := conn.PNForLID(ctx, peer); pn.User != "" {
				peer = pn
			}
		}
		queue.add(peer)
	})
	return func() { conn.RemoveEventHandler(handlerID) }
}

func openGroupOffer(ctx context.Context, conn saleConn, send sendFunc, log *slog.Logger, group, offer string) (*offerQueue, func(), error) {
	jid, name, err := findGroup(ctx, conn, group)
	if err != nil {
		return nil, nil, err
	}
	o := postedOffer{group: jid, id: conn.GenerateMessageID(), postedAt: time.Now().Truncate(time.Second)}
	queue := newOfferQueue()
	closeOffer := watchOfferReplies(ctx, conn, queue, []postedOffer{o})
	if _, err := send(ctx, jid, &waE2E.Message{Conversation: proto.String(offer)}, whatsmeow.SendRequestExtra{ID: o.id}); err != nil {
		closeOffer()
		return nil, nil, fmt.Errorf("group offer send failed or is uncertain: %w", err)
	}
	log.Info("group offer sent; waiting for a private reply", "group", name, "offer_id", o.id)
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
