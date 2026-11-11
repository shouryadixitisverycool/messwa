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
	o, err := openGroupOffer(ctx, c, log, []string{group}, offer, 0, nil)
	if err != nil {
		return types.EmptyJID, err
	}
	defer o.close()
	return o.queue.next(ctx)
}

type offerQueue struct {
	mu     sync.Mutex
	buyers []types.JID
	seen   map[types.JID]bool
	ready  chan struct{}
	closed bool
	// names holds each buyer's WhatsApp push name from their first reply.
	names map[types.JID]string
}

// addNamed queues a buyer and remembers the name on their first reply.
func (q *offerQueue) addNamed(peer types.JID, name string) {
	q.mu.Lock()
	if !q.closed && !q.seen[peer] && name != "" {
		if q.names == nil {
			q.names = make(map[types.JID]string)
		}
		q.names[peer] = name
	}
	q.mu.Unlock()
	q.add(peer)
}

func (q *offerQueue) name(peer types.JID) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.names[peer]
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

// offerTarget is a joined group an offer goes to.
type offerTarget struct {
	JID  types.JID
	Name string
}

// groupOffer is one offer posted to one or more groups. Every copy feeds the
// same buyer queue, so the first private reply to any copy reserves the meal
// and a buyer who replies to two copies is queued once.
type groupOffer struct {
	queue *offerQueue

	mu sync.Mutex
	// copies are every copy that may be in a group, including sends that
	// failed or are uncertain, so replies to them still count.
	copies []postedOffer
	posted []postedOffer

	stopPosting   context.CancelFunc
	donePosting   chan struct{}
	removeHandler func()
}

// stop posts no further copies, waits for a send in flight, and returns
// every copy WhatsApp acknowledged.
func (o *groupOffer) stop() []postedOffer {
	o.stopPosting()
	<-o.donePosting
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]postedOffer(nil), o.posted...)
}

// close stops posting and stops listening for replies.
func (o *groupOffer) close() []postedOffer {
	posted := o.stop()
	o.removeHandler()
	return posted
}

func (o *groupOffer) matches(e *events.Message) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, p := range o.copies {
		if privateOfferReply(e, p.Chat, p.ID, p.At) {
			return true
		}
	}
	return false
}

// openGroupOffer posts offer to each group in order, gap apart, and returns
// once the first copy is posted. Later copies go out in the background until
// stop or close; a sale doesn't wait for them. onPosted, if set, runs for each
// copy WhatsApp acknowledges, before stop returns.
func openGroupOffer(ctx context.Context, c *wa.Client, log *slog.Logger, groups []string, offer string, gap time.Duration, onPosted func(postedOffer)) (*groupOffer, error) {
	if len(groups) == 0 {
		return nil, errors.New("no group to post the offer to")
	}
	joined, err := c.Groups(ctx)
	if err != nil {
		return nil, err
	}
	targets := make([]offerTarget, 0, len(groups))
	for _, g := range groups {
		target, err := pickGroup(joined, g)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}

	postCtx, stopPosting := context.WithCancel(ctx)
	o := &groupOffer{
		queue:       &offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)},
		stopPosting: stopPosting,
		donePosting: make(chan struct{}),
	}
	handlerID := c.WA.AddEventHandler(func(evt any) {
		e, ok := evt.(*events.Message)
		if !ok || !o.matches(e) || buyerPassed(e.Message) {
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
		o.queue.addNamed(peer, e.Info.PushName)
	})
	o.removeHandler = func() { c.WA.RemoveEventHandler(handlerID) }

	post := func(ctx context.Context, g offerTarget) (postedOffer, error) {
		p := postedOffer{Chat: g.JID, GroupName: g.Name, ID: c.WA.GenerateMessageID(), At: time.Now().Truncate(time.Second), Text: offer}
		// Listen before sending so an instant reply still matches.
		o.mu.Lock()
		o.copies = append(o.copies, p)
		o.mu.Unlock()
		resp, err := c.WA.SendMessage(ctx, g.JID, &waE2E.Message{Conversation: proto.String(offer)}, whatsmeow.SendRequestExtra{ID: p.ID})
		// Replies match against the local time above; the ledger and group
		// log get WhatsApp's.
		p.At = firstTime(resp.Timestamp, p.At)
		return p, err
	}
	first := make(chan struct{})
	var firstOnce sync.Once
	var postErr error
	go func() {
		defer close(o.donePosting)
		_, postErr = postOfferCopies(postCtx, targets, gap, post, func(p postedOffer) {
			o.mu.Lock()
			o.posted = append(o.posted, p)
			o.mu.Unlock()
			log.Info("group offer sent; waiting for a private reply", "group", p.GroupName, "offer_id", p.ID)
			if onPosted != nil {
				onPosted(p)
			}
			firstOnce.Do(func() { close(first) })
		})
		if postErr != nil {
			log.Warn("group offer send failed or is uncertain; replies to that copy still count", "err", postErr)
		}
	}()
	select {
	case <-first:
		return o, nil
	case <-o.donePosting:
		o.mu.Lock()
		posted := len(o.posted)
		o.mu.Unlock()
		if posted > 0 {
			return o, nil
		}
		o.removeHandler()
		if postErr == nil {
			postErr = ctx.Err()
		}
		return nil, fmt.Errorf("group offer send failed or is uncertain: %w", postErr)
	}
}

// postOfferCopies posts one copy per group, in order, waiting gap between
// sends. A failed send doesn't stop the rest. It stops early when ctx ends and
// returns every copy that was acknowledged, plus the send errors.
func postOfferCopies(ctx context.Context, groups []offerTarget, gap time.Duration, post func(context.Context, offerTarget) (postedOffer, error), posted func(postedOffer)) ([]postedOffer, error) {
	var copies []postedOffer
	var errs []error
	for i, g := range groups {
		if i > 0 {
			timer := time.NewTimer(gap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return copies, errors.Join(errs...)
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			break
		}
		p, err := post(ctx, g)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", g.Name, err))
			continue
		}
		copies = append(copies, p)
		if posted != nil {
			posted(p)
		}
	}
	return copies, errors.Join(errs...)
}

// pickGroup finds one joined group by exact name or JID.
func pickGroup(joined []wa.GroupSummary, group string) (offerTarget, error) {
	var selected *wa.GroupSummary
	for i := range joined {
		g := &joined[i]
		if g.JID != group && (strings.HasSuffix(group, "@g.us") || g.Name != group) {
			continue
		}
		if selected != nil {
			return offerTarget{}, fmt.Errorf("group name %q is ambiguous; use its exact JID", group)
		}
		selected = g
	}
	if selected == nil {
		return offerTarget{}, fmt.Errorf("no joined group matches %q", group)
	}
	jid, err := types.ParseJID(selected.JID)
	if err != nil || jid.Server != types.GroupServer || jid.User == "" {
		return offerTarget{}, errors.New("selected group has an invalid JID")
	}
	return offerTarget{JID: jid, Name: selected.Name}, nil
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
