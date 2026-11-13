package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/repost"
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
	targets, err := findGroups(ctx, conn, []string{group})
	if err != nil {
		return types.EmptyJID, err
	}
	o := watchGroupOffer(ctx, conn, newOfferQueue())
	defer o.close()
	o.post(ctx, log, targets, 0, 0, directCopy(conn, c.WA.SendMessage, offer), nil)
	if err := o.waitPosted(); err != nil {
		return types.EmptyJID, err
	}
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
	// join, when set, records a new buyer before they are queued, with the
	// chat they replied from. A buyer it refuses is dropped.
	join func(peer, chat types.JID) bool
	// firsts holds each buyer's first reply, for a negotiation to answer.
	firsts map[types.JID]buyerMsg
	// prio orders the queue: next returns the waiting buyer with the
	// highest one, first come first served among equals. A negotiation's
	// pay queue sets it to the agreed price.
	prio map[types.JID]int64
}

func newOfferQueue() *offerQueue {
	return &offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
}

func (q *offerQueue) add(peer types.JID) { q.addFrom(peer, peer, "") }

// addFrom queues peer, who replied from chat (their LID chat, say), and
// remembers the push name from their first reply.
func (q *offerQueue) addFrom(peer, chat types.JID, name string) bool {
	return q.addAfter(peer, chat, name, nil)
}

// addAfter is addFrom with a step between recording peer and queueing
// them, such as a first message to someone who never wrote. before runs
// with peer recorded by join and seen, so their replies already count; if
// it fails, peer stays seen but never reaches the queue.
func (q *offerQueue) addAfter(peer, chat types.JID, name string, before func() bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.seen[peer] {
		return false
	}
	if q.join != nil && !q.join(peer, chat) {
		return false
	}
	q.seen[peer] = true
	if name != "" {
		if q.names == nil {
			q.names = make(map[types.JID]string)
		}
		q.names[peer] = name
	}
	if before != nil {
		q.mu.Unlock()
		ok := before()
		q.mu.Lock()
		if !ok || q.closed {
			return false
		}
	}
	q.buyers = append(q.buyers, peer)
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return true
}

// noteFirst remembers peer's first reply, unless one is already noted.
func (q *offerQueue) noteFirst(peer types.JID, id, text string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.firsts == nil {
		q.firsts = make(map[types.JID]buyerMsg)
	}
	if _, ok := q.firsts[peer]; !ok && !q.seen[peer] {
		q.firsts[peer] = buyerMsg{id: id, text: text}
	}
}

// first is peer's first reply: its message ID and text.
func (q *offerQueue) first(peer types.JID) (string, string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	m := q.firsts[peer]
	return m.id, m.text
}

// has reports whether peer ever joined the queue.
func (q *offerQueue) has(peer types.JID) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.seen[peer]
}

// joined reports whether anyone ever joined the queue.
func (q *offerQueue) joined() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.seen) > 0
}

// isClosed reports whether the queue stopped taking buyers.
func (q *offerQueue) isClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

// waiting is how many buyers are queued.
func (q *offerQueue) waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buyers)
}

func (q *offerQueue) name(peer types.JID) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.names[peer]
}

// requeue queues peer even if they were queued before, as when a buyer
// whose payment turn lapsed agrees again. It does nothing once the queue
// is closed or while peer is already waiting.
func (q *offerQueue) requeue(peer types.JID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || slices.Contains(q.buyers, peer) {
		return
	}
	q.seen[peer] = true
	q.buyers = append(q.buyers, peer)
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// setPriority sets peer's place in line; see prio.
func (q *offerQueue) setPriority(peer types.JID, p int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.prio == nil {
		q.prio = make(map[types.JID]int64)
	}
	q.prio[peer] = p
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
			best := 0
			for i, b := range q.buyers {
				if q.prio[b] > q.prio[q.buyers[best]] {
					best = i
				}
			}
			peer := q.buyers[best]
			q.buyers = slices.Delete(q.buyers, best, best+1)
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
	JID types.JID
	// Name is the group's subject, or how the config names it when the
	// group list returned none.
	Name string
	// listed is the group as the joined-groups list described it, fresh
	// as a direct query did just before posting.
	listed, fresh *types.GroupInfo
}

// findGroups resolves exact group names or JIDs among the joined groups.
func findGroups(ctx context.Context, conn saleConn, groups []string) ([]offerTarget, error) {
	if len(groups) == 0 {
		return nil, errors.New("no group to post the offer to")
	}
	joined, err := conn.GetJoinedGroups(ctx)
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
	return targets, nil
}

// pickGroup finds one joined group by exact name or JID.
func pickGroup(joined []*types.GroupInfo, group string) (offerTarget, error) {
	var selected *types.GroupInfo
	for _, g := range joined {
		if g.JID.String() != group && (strings.HasSuffix(group, "@g.us") || g.Name != group) {
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
	if selected.JID.Server != types.GroupServer || selected.JID.User == "" {
		return offerTarget{}, errors.New("selected group has an invalid JID")
	}
	name := selected.Name
	if name == "" {
		name = group
	}
	return offerTarget{JID: selected.JID, Name: name, listed: selected}, nil
}

// groupAttrs describes a group's metadata for the log: what decides how
// whatsmeow addresses a group message. No names or members.
func groupAttrs(prefix string, g *types.GroupInfo) []any {
	if g == nil {
		return nil
	}
	return []any{
		prefix + "addressing_mode", string(g.AddressingMode),
		prefix + "participants", len(g.Participants),
		prefix + "size", g.ParticipantCount,
		prefix + "has_subject", g.Name != "",
		prefix + "announce", g.IsAnnounce,
		prefix + "default_subgroup", g.IsDefaultSubGroup,
		prefix + "parent", g.LinkedParentJID.String(),
	}
}

// refreshGroup asks WhatsApp for g's details right before a copy goes out.
// whatsmeow builds a group message from the details it cached last
// (addressing mode and members), and the joined-groups list overwrites
// that cache. The list can describe a group only partly: it gave one
// community subgroup no subject, and every post to that group failed with
// error 479 (invalid stanza). A partial cache entry is the suspected
// cause. A direct query replaces the entry with the group's full details.
func refreshGroup(ctx context.Context, conn saleConn, log *slog.Logger, g offerTarget) offerTarget {
	info, err := conn.GetGroupInfo(ctx, g.JID)
	if err != nil {
		log.Warn("could not refresh the group's details before posting; sending with the group list's", "group", g.Name, "jid", g.JID, "err", err)
		return g
	}
	g.fresh = info
	if info.Name != "" && g.listed != nil && g.listed.Name == "" {
		g.Name = info.Name
	}
	log.Debug("group details before posting", append(append([]any{"group", g.Name, "jid", g.JID}, groupAttrs("listed_", g.listed)...), groupAttrs("", info)...)...)
	if g.listed != nil && (g.listed.AddressingMode != info.AddressingMode || len(g.listed.Participants) != len(info.Participants)) {
		gap := listGap{listedMode: g.listed.AddressingMode, mode: info.AddressingMode, listedN: len(g.listed.Participants), n: len(info.Participants)}
		level := slog.LevelDebug
		if listGaps.note(g.JID, gap) {
			level = slog.LevelInfo
		}
		log.Log(ctx, level, "the group list described this group differently; posting with the fresh details", "group", g.Name, "jid", g.JID,
			"listed_addressing_mode", string(gap.listedMode), "addressing_mode", string(gap.mode),
			"listed_participants", gap.listedN, "participants", gap.n)
	}
	return g
}

// listGap is how the joined-groups list and a direct query disagreed
// about a group.
type listGap struct {
	listedMode, mode types.AddressingMode
	listedN, n       int
}

// listGapLog remembers, per group, the last disagreement logged at info.
//
// The disagreement isn't a stale cache that one refresh could fix. Every
// findGroups or postCopies call asks WhatsApp for the joined-groups list
// again, and for "Mess Buy Sell @ IIITH - 2" (a community subgroup with
// about 1025 members) WhatsApp answers each time with no addressing mode
// and no members. whatsmeow writes that partial entry over its group cache
// on every list call, and refreshGroup's direct query writes the full one
// back before each copy. So the mismatch comes back on every post, and
// logging it at info each time says nothing new. It logs at info the first
// time per group and whenever the details change, and at debug otherwise.
type listGapLog struct {
	mu   sync.Mutex
	seen map[types.JID]listGap
}

var listGaps = &listGapLog{seen: map[types.JID]listGap{}}

// note records gap for jid and reports whether it is new for that group.
func (l *listGapLog) note(jid types.JID, gap listGap) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.seen[jid]; ok && last == gap {
		return false
	}
	l.seen[jid] = gap
	return true
}

// groupOffer is one offer posted as one or more copies, one per group.
// Every copy feeds the same buyer queue, so the first private reply to any
// copy reserves the meal and a buyer who replies to two copies is queued
// once.
type groupOffer struct {
	queue *offerQueue
	conn  saleConn

	mu sync.Mutex
	// copies are every copy that may be in a group, including sends that
	// failed or are uncertain, so replies to them still count.
	copies []postedOffer
	posted []postedOffer
	// plain, when set, lets DMs about the offer that don't quote it make
	// their sender a buyer. sentDM is when you last messaged each chat
	// while the offer was up. See plain_dm.go.
	plain  *plainDMs
	sentDM map[types.JID]time.Time
	// adopt, when set, takes private replies to posts that aren't this
	// offer's: a cancelled task's posts for the same meal.
	adopt func(*events.Message) bool

	stopPosting   context.CancelFunc
	donePosting   chan struct{}
	firstPosted   chan struct{}
	firstOnce     sync.Once
	postErr       error
	removeHandler func()

	// Reposting runs beside posting and stops with it; see keepInView.
	stopped       bool
	stopReposting context.CancelFunc
	doneReposting chan struct{}
}

// watchGroupOffer queues everyone who replies privately to a copy of the
// offer, by phone number when WhatsApp tells us one. Copies are added with
// watch, or posted with post.
func watchGroupOffer(ctx context.Context, conn saleConn, queue *offerQueue) *groupOffer {
	done := make(chan struct{})
	close(done)
	o := &groupOffer{queue: queue, conn: conn, stopPosting: func() {}, donePosting: done, firstPosted: make(chan struct{}),
		stopReposting: func() {}, doneReposting: done}
	handlerID := conn.AddEventHandler(func(evt any) {
		e, ok := evt.(*events.Message)
		if !ok {
			return
		}
		if peer, ok := o.buyer(ctx, conn, e); ok {
			queue.noteFirst(peer, e.Info.ID, messageText(e.Message))
			queue.addFrom(peer, e.Info.Chat.ToNonAD(), e.Info.PushName)
		}
	})
	o.removeHandler = func() { conn.RemoveEventHandler(handlerID) }
	return o
}

// buyerPeer is who sent a DM: their phone-number JID when WhatsApp tells us
// one, else the chat's JID (a LID).
func buyerPeer(ctx context.Context, conn saleConn, info types.MessageInfo) types.JID {
	peer := info.Chat.ToNonAD()
	if alt := info.SenderAlt.ToNonAD(); alt.User != "" && alt.Server == types.DefaultUserServer {
		return alt
	}
	if peer.Server == types.HiddenUserServer {
		if pn := conn.PNForLID(ctx, peer); pn.User != "" {
			return pn
		}
	}
	return peer
}

// acceptPlainDMs lets DMs about the offer that don't quote it make their
// sender a buyer too. Call it before posting.
func (o *groupOffer) acceptPlainDMs(p *plainDMs) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.plain = p
}

// adoptReplies lets adopt claim private replies to posts that aren't this
// offer's. Call it before posting.
func (o *groupOffer) adoptReplies(adopt func(*events.Message) bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.adopt = adopt
}

// buyer decides whether e makes its sender a buyer, and returns them by
// phone number when WhatsApp tells us one. Every way into the queue comes
// through here: a private reply quoting a copy of the offer, or, with plain
// DMs on, a DM asking about it from someone you weren't chatting with.
func (o *groupOffer) buyer(ctx context.Context, conn saleConn, e *events.Message) (types.JID, bool) {
	o.mu.Lock()
	o.noteSent(e)
	quoted := o.quotesCopy(e)
	plain := !quoted && o.plainCandidate(e)
	p, adopt := o.plain, o.adopt
	o.mu.Unlock()
	if !quoted && !plain && adopt != nil && adopt(e) {
		quoted = true
	}
	if !quoted && !plain || buyerPassed(e.Message) {
		return types.EmptyJID, false
	}
	peer := buyerPeer(ctx, conn, e.Info)
	if plain {
		if o.chatting(ctx, e, peer) {
			return types.EmptyJID, false
		}
		p.log.Info("a DM asks about the offer without quoting it; its sender joins the queue unless already in it", "message_id", e.Info.ID)
	}
	return peer, true
}

// watch queues replies to p from now on.
func (o *groupOffer) watch(p postedOffer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.copies = append(o.copies, p)
}

// matches reports whether e is a private reply quoting a copy of the offer.
func (o *groupOffer) matches(e *events.Message) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.quotesCopy(e)
}

// quotesCopy is matches with o.mu held.
func (o *groupOffer) quotesCopy(e *events.Message) bool {
	for _, p := range o.copies {
		if privateOfferReply(e, p.Chat, p.ID, p.At) {
			return true
		}
	}
	return false
}

// sendCopy sends copy number seq of the offer to g: 0 for the first copy
// there, one more for each repost. It must watch the copy before sending,
// so an instant reply still matches.
type sendCopy func(ctx context.Context, o *groupOffer, g offerTarget, seq int) (postedOffer, error)

// directCopy posts text with send, for deliver-qr and offer-group.
func directCopy(conn saleConn, send sendFunc, text string) sendCopy {
	return func(ctx context.Context, o *groupOffer, g offerTarget, _ int) (postedOffer, error) {
		p := postedOffer{Chat: g.JID, GroupName: g.Name, ID: conn.GenerateMessageID(), At: time.Now().Truncate(time.Second), Text: text}
		o.watch(p)
		resp, err := send(ctx, g.JID, &waE2E.Message{Conversation: proto.String(text)}, whatsmeow.SendRequestExtra{ID: p.ID})
		// Replies match against the local time above; the ledger and the
		// group log get WhatsApp's.
		p.At = firstTime(resp.Timestamp, p.At)
		return p, err
	}
}

// post sends a copy to each target in order in the background, the first
// after delay and the rest gap apart. A copy that fails doesn't stop the
// rest. Posting ends with stop or close; a sale doesn't wait for it.
// onPosted, if set, runs for each copy WhatsApp acknowledges, before stop
// returns. Call post at most once.
func (o *groupOffer) post(ctx context.Context, log *slog.Logger, targets []offerTarget, delay, gap time.Duration, send sendCopy, onPosted func(postedOffer)) {
	postCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	o.stopPosting, o.donePosting = stop, done
	go func() {
		defer close(done)
		if delay > 0 && len(targets) > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-postCtx.Done():
				timer.Stop()
				o.postErr = postCtx.Err()
				return
			case <-timer.C:
			}
		}
		failed := 0
		post := func(ctx context.Context, g offerTarget) (postedOffer, error) {
			g = refreshGroup(ctx, o.conn, log, g)
			p, err := send(ctx, o, g, 0)
			if err != nil {
				failed++
				log.Warn("group offer copy failed or is uncertain; replies to it still count",
					append(append([]any{"group", g.Name, "jid", g.JID, "err", err}, groupAttrs("listed_", g.listed)...), groupAttrs("", g.fresh)...)...)
			}
			return p, err
		}
		copies, err := postOfferCopies(postCtx, targets, gap, post, func(p postedOffer) {
			o.addPosted(p)
			log.Info("group offer sent; waiting for a private reply", "group", p.GroupName, "offer_id", p.ID)
			if onPosted != nil {
				onPosted(p)
			}
			o.firstOnce.Do(func() { close(o.firstPosted) })
		})
		if err != nil {
			log.Warn("group offer posting finished with failures", "posted", len(copies), "failed", failed)
		} else if postCtx.Err() != nil {
			err = postCtx.Err()
		}
		o.postErr = err
	}()
}

// waitPosted returns once a copy is acknowledged, or an error when posting
// ended without one.
func (o *groupOffer) waitPosted() error {
	select {
	case <-o.firstPosted:
		return nil
	case <-o.donePosting:
	}
	select {
	case <-o.firstPosted:
		return nil
	default:
	}
	if o.postErr == nil {
		return errors.New("no group to post the offer to")
	}
	return fmt.Errorf("group offer send failed or is uncertain: %w", o.postErr)
}

// addPosted notes a copy WhatsApp acknowledged.
func (o *groupOffer) addPosted(p postedOffer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.posted = append(o.posted, p)
}

// keepInView starts r reposting the offer once the first round of copies
// is done. It stops with stop or close, or when ctx ends. Call it at most
// once.
func (o *groupOffer) keepInView(ctx context.Context, r *reposter) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped || !r.enabled() {
		return
	}
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	o.stopReposting, o.doneReposting = stop, done
	posting := o.donePosting
	go func() {
		defer close(done)
		select {
		case <-posting:
		case <-ctx.Done():
			return
		}
		r.run(ctx, o)
	}()
}

// memoryCopies lists every copy this offer sent, by group, numbered in
// order. A copy WhatsApp didn't acknowledge counts as failed. deliver-qr
// reposts from this; a task reads its copies from the store instead.
func (o *groupOffer) memoryCopies(context.Context) ([]repost.Copy, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	acked := map[string]bool{}
	for _, p := range o.posted {
		acked[p.ID] = true
	}
	seqs := map[types.JID]int{}
	var out []repost.Copy
	for _, p := range o.copies {
		state := repost.Failed
		if acked[p.ID] {
			state = repost.Posted
		}
		out = append(out, repost.Copy{Group: p.Chat.String(), Seq: seqs[p.Chat], ID: p.ID, At: p.At, State: state})
		seqs[p.Chat]++
	}
	return out, nil
}

// repostsDone is closed once no more copies will go out: posting and
// reposting have both finished.
func (o *groupOffer) repostsDone() <-chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.doneReposting
}

// lastPosted is when the newest acknowledged copy went out, or zero.
func (o *groupOffer) lastPosted() time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	var last time.Time
	for _, p := range o.posted {
		if p.At.After(last) {
			last = p.At
		}
	}
	return last
}

// stop posts no further copies, waits for a send in flight, and returns
// every copy WhatsApp acknowledged.
func (o *groupOffer) stop() []postedOffer {
	o.mu.Lock()
	o.stopped = true
	stopReposting, doneReposting := o.stopReposting, o.doneReposting
	o.mu.Unlock()
	o.stopPosting()
	stopReposting()
	<-o.donePosting
	<-doneReposting
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
