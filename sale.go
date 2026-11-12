package main

import (
	"context"
	"errors"
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
	"messwa/internal/meals"
	"messwa/internal/portal"
	"messwa/internal/store"
	"messwa/internal/wa"
)

// saleConn is the part of the WhatsApp client a sale uses. deliver-qr
// passes its own connection and sell tasks pass run's. Tests fake it.
type saleConn interface {
	AddEventHandler(whatsmeow.EventHandler) uint32
	RemoveEventHandler(uint32) bool
	GenerateMessageID() types.MessageID
	Upload(ctx context.Context, data []byte, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	SendPeerMessage(ctx context.Context, msg *waE2E.Message) (whatsmeow.SendResponse, error)
	BuildUnavailableMessageRequest(chat, sender types.JID, id string) *waE2E.Message
	BuildReaction(chat, sender types.JID, id types.MessageID, reaction string) *waE2E.Message
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	// PNForLID returns the phone-number JID for a LID, or an empty JID.
	PNForLID(ctx context.Context, lid types.JID) types.JID
	// SavedContact reports whether any of jids, under its number or its
	// LID, is in your address book.
	SavedContact(ctx context.Context, jids ...types.JID) bool
}

// waConn adapts a whatsmeow client to saleConn.
type waConn struct{ *whatsmeow.Client }

func (c waConn) PNForLID(ctx context.Context, lid types.JID) types.JID {
	pn, err := c.Store.LIDs.GetPNForLID(ctx, lid)
	if err != nil {
		return types.EmptyJID
	}
	return pn.ToNonAD()
}

func (c waConn) SavedContact(ctx context.Context, jids ...types.JID) bool {
	saved, _ := wa.SavedContact(ctx, c.Store, jids...)
	return saved
}

// sendFunc sends one chat message. Sell tasks pass a gate that enforces
// their rules; deliver-qr sends directly.
type sendFunc func(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)

// mealPortal is the part of portal.Client a sale uses.
type mealPortal interface {
	RequireRegistration(ctx context.Context, date, meal string) (portal.Registration, error)
	QRForMeal(ctx context.Context, date, meal, accountHash string) (portal.CurrentQR, error)
}

var errBuyerPassed = errors.New("buyer passed on the meal")
var errBuyerTimedOut = errors.New("buyer payment window expired")

// errHandedOff ends a sale whose first buyer was handed to you.
var errHandedOff = errors.New("sale handed off to you")

// sale sells one meal over an open WhatsApp connection. deliver-qr and sell
// tasks both drive it, so the payment checks, the one-delivery guard in the
// store and the QR send are the same for both.
type sale struct {
	log  *slog.Logger
	loc  *time.Location
	conn saleConn
	st   *store.Store
	// send carries every chat message. sendPaid carries only the QR after
	// a payment was claimed, which must go out even if the task was
	// cancelled a moment later.
	send, sendPaid sendFunc

	date, meal string
	end        time.Time
	imageHash  string
	mess, diet string

	// With a portal, the QR is fetched right before each upload, and the
	// registration is checked again before each buyer's turn.
	portal      mealPortal
	accountHash string
	portalMess  string
	// Without a portal, this image is sent. testImage marks the embedded
	// placeholder, so the payment prompt says it's a test.
	image     []byte
	testImage bool

	diagnostics      bool
	assumeUnreadable bool
	// acceptImages counts an image from the buyer whose turn it is as
	// their payment, once they have the payment prompt. Nothing about the
	// image is checked. See [sell] accept_payment_images.
	acceptImages bool

	// handoff, when set, gets the first buyer from the queue instead of the
	// bot, which sends them nothing. others are the buyers still queued.
	// sellToQueue then returns errHandedOff.
	handoff func(ctx context.Context, buyer types.JID, d store.Delivery, others []types.JID) error
	// sold, when set, runs once the QR is out, before the queue hears
	// "sorry sold". Group offers stop posting copies here.
	sold func()
	// others, when set, are more buyers to tell "sorry sold" besides the
	// queue: a negotiating task's buyers who are still talking.
	others func() []types.JID
}

// findSlot returns the configured meal's window on date.
func findSlot(cfg *config.Config, date, meal string) (meals.Slot, error) {
	day, err := time.ParseInLocation("2006-01-02", date, cfg.Location())
	if err != nil {
		return meals.Slot{}, errors.New("date must be YYYY-MM-DD")
	}
	for _, w := range meals.New(cfg).Windows(day) {
		if w.Meal == meal {
			return w, nil
		}
	}
	return meals.Slot{}, errors.New("meal must name a configured meal")
}

// usePortal checks the meal's registration and binds the sale to the
// portal account, not the QR token that rotates daily.
func (s *sale) usePortal(ctx context.Context, p mealPortal, vegOnly func(string) bool) error {
	current, err := p.QRForMeal(ctx, s.date, s.meal, s.accountHash)
	if err != nil {
		return err
	}
	s.portal = p
	s.accountHash = current.AccountHash
	s.portalMess = current.Registration.Mess
	s.mess, s.diet = current.Registration.MessDiet()
	if s.diet == "" && vegOnly(s.mess) {
		s.diet = "veg"
	}
	s.imageHash = "portal:" + s.accountHash
	return nil
}

// checkRegistration makes sure the portal registration is still there and
// unchanged before a buyer is asked to pay.
func (s *sale) checkRegistration(ctx context.Context) error {
	if s.portal == nil {
		return nil
	}
	reg, err := s.portal.RequireRegistration(ctx, s.date, s.meal)
	if err != nil {
		return err
	}
	if reg.Mess != s.portalMess {
		return errors.New("portal registration changed since the group offer")
	}
	return nil
}

func (s *sale) reservation(peer types.JID, amount string) store.Delivery {
	return store.Delivery{Date: s.date, Meal: s.meal, Peer: peer.String(), Amount: amount, ImageHash: s.imageHash, ArmedAt: time.Now(), Mess: s.mess, Diet: s.diet}
}

func (s *sale) sendQR(ctx context.Context, recipient types.JID, id string, d store.Delivery, queued bool) error {
	data := s.image
	if s.portal != nil {
		current, err := s.portal.QRForMeal(ctx, d.Date, d.Meal, s.accountHash)
		if err != nil {
			return err
		}
		if queued && current.Registration.Mess != s.portalMess {
			return errors.New("portal registration changed during the sale")
		}
		data = current.PNG
	}
	data, mime, width, height, err := deliveryImageBytes(data)
	if err != nil {
		return err
	}
	up, err := s.conn.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return err
	}
	_, err = s.sendPaid(ctx, recipient, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String(fmt.Sprintf("%s %s", d.Date, d.Meal)), Mimetype: proto.String(mime), Width: proto.Uint32(uint32(width)), Height: proto.Uint32(uint32(height)),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength,
	}}, whatsmeow.SendRequestExtra{ID: id})
	return err
}

// turn is one buyer's chance to pay.
type turn struct {
	// arm reserves the meal for this buyer in the store and returns when it
	// was first armed. Only payments after that count.
	arm func(context.Context, store.Delivery) (time.Time, error)
	// listening runs once the payment handler is registered, before the
	// prompt. deliver-qr -peer connects here, so a payment queued while it
	// was offline is not missed.
	listening func() error
	// prompt is sent to the buyer after arming. Empty sends nothing.
	prompt string
	// prompting claims the prompt before it is sent. Returning false skips
	// the send: it already went out before a restart.
	prompting func(context.Context) (bool, error)
	// payUntil limits the turn for a queued buyer, who may also reply "not
	// interested". Zero waits until the meal ends.
	payUntil time.Time
	// amounts are more prices the buyer may pay besides d.Amount, all at or
	// above the floor. reprice changes the reservation to the one they paid
	// before it is claimed, so the ledger records it.
	amounts []string
	reprice func(context.Context, store.Delivery, string) (store.Delivery, error)
}

// takeTurn arms the meal for buyer and delivers the QR after a matching
// payment. It returns nil once the QR is sent, errBuyerPassed or
// errBuyerTimedOut when a queued buyer's turn ends without one, or another
// error that stops the sale.
func (s *sale) takeTurn(ctx context.Context, buyer types.JID, d store.Delivery, t turn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	armedAt, err := t.arm(ctx, d)
	if err != nil {
		return err
	}
	// WhatsApp event timestamps have second precision. Start at the next second
	// so messages from just before the first arming cannot qualify.
	armedAt = armedAt.Add(time.Second)
	queued := !t.payUntil.IsZero()
	payUntil := t.payUntil
	var deadline <-chan time.Time
	if queued {
		if s.end.Before(payUntil) {
			payUntil = s.end
		}
		timer := time.NewTimer(time.Until(payUntil))
		defer timer.Stop()
		deadline = timer.C
	}
	done := make(chan error, 1)
	recoveryDone := make(chan struct{}, 1)
	pending := 0
	var mu sync.Mutex
	attempted, delivering := false, false
	// prompted is set once the buyer has the payment prompt. A turn
	// without one (deliver-qr -peer without -offer-text, or one resumed
	// after a restart) had its price settled before it started.
	prompted := t.prompt == ""
	requestedPayments := map[string]context.CancelFunc{}
	// finish runs with mu held. The delivery itself runs in a goroutine so
	// the event handler doesn't hold up the rest of run's ingest.
	finish := func(e *events.Message, amountMissing bool, paidAmount string, image bool) {
		attempted, delivering = true, true
		for _, stop := range requestedPayments {
			stop()
		}
		if amountMissing {
			s.log.Warn("payment amount unavailable after recovery; assuming agreed price",
				"date", d.Date, "meal", d.Meal, "amount_assumed", d.Amount, "payment_id", e.Info.ID)
		}
		id := s.conn.GenerateMessageID()
		paid := store.Payment{ID: e.Info.ID, At: e.Info.Timestamp, BuyerName: e.Info.PushName}
		if image {
			paid.Kind = store.PaidByImage
			s.log.Warn("buyer sent an image after the payment prompt; assuming it shows a payment, which is NOT verified. Check your UPI app",
				"date", d.Date, "meal", d.Meal, "amount_assumed", d.Amount, "image_id", e.Info.ID)
		}
		go func() {
			// Once a payment is claimed, cancelling the task or the meal
			// ending must not cut the QR upload short.
			sendCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer stop()
			d := d
			if paidAmount != "" && paidAmount != d.Amount {
				if t.reprice == nil {
					done <- errors.New("payment matched a quoted price but the reservation cannot be repriced")
					return
				}
				var err error
				if d, err = t.reprice(sendCtx, d, paidAmount); err != nil {
					done <- err
					return
				}
			}
			s.log.Info("payment assumed; sending meal image", "date", d.Date, "meal", d.Meal, "amount", d.Amount, "paid_by", paidBy(paid.Kind))
			err := deliverOnce(sendCtx, s.st, d, paid, id, func() error { return s.sendQR(sendCtx, e.Info.Chat, id, d, queued) })
			if err == nil {
				s.log.Info("meal image sent", "date", d.Date, "meal", d.Meal, "message_id", id)
			}
			done <- err
		}()
	}
	matchesBuyer := func(info types.MessageInfo) bool {
		if info.IsFromMe || info.IsGroup {
			return false
		}
		if info.Chat.ToNonAD() == buyer || info.SenderAlt.ToNonAD() == buyer {
			return true
		}
		if info.Chat.Server == types.HiddenUserServer && buyer.Server == types.DefaultUserServer {
			return s.conn.PNForLID(ctx, info.Chat.ToNonAD()) == buyer
		}
		return false
	}
	handler := s.conn.AddEventHandler(func(evt any) {
		if s.diagnostics {
			switch e := evt.(type) {
			case *events.Connected:
				s.log.Info("WhatsApp connected")
			case *events.Disconnected:
				s.log.Warn("WhatsApp disconnected")
			case *events.LoggedOut:
				s.log.Warn("WhatsApp session logged out")
			case *events.UndecryptableMessage:
				if matchesBuyer(e.Info) {
					s.log.Warn("could not decrypt buyer message", "at", e.Info.Timestamp, "unavailable", e.IsUnavailable)
				}
			}
		}
		e, ok := evt.(*events.Message)
		if !ok {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if attempted || ctx.Err() != nil || !matchesBuyer(e.Info) {
			return
		}
		stopRecovery := requestedPayments[e.Info.ID]
		if queued && (!e.Info.Timestamp.Before(payUntil) || (stopRecovery == nil && !time.Now().Before(payUntil))) {
			return
		}
		if queued && pending == 0 && !e.Info.Timestamp.Before(armedAt) && buyerPassed(e.Message) {
			attempted = true
			done <- errBuyerPassed
			return
		}
		if s.acceptImages && e.Message.GetImageMessage() != nil {
			reason := imagePaymentRejection(e, armedAt, s.end, time.Now())
			if reason == nil && !prompted {
				reason = errors.New("image arrived before the payment prompt went out")
			}
			if reason != nil {
				s.log.Info("image did not count as payment", "image_id", e.Info.ID, "at", e.Info.Timestamp, "reason", reason.Error())
				return
			}
			finish(e, false, "", true)
			return
		}
		paidAmount, reason := paymentMatch(e, append([]string{d.Amount}, t.amounts...), armedAt, s.end, time.Now())
		if !errors.Is(reason, errTruncatedPayment) && stopRecovery != nil {
			stopRecovery()
		}
		if reason != nil {
			if errors.Is(reason, errTruncatedPayment) && stopRecovery == nil {
				recoveryCtx, cancelRecovery := context.WithCancel(ctx)
				requestedPayments[e.Info.ID] = cancelRecovery
				pending++
				if s.diagnostics {
					wire := e.Message.GetSendPaymentMessage()
					s.log.Info("incomplete payment fields", "message_id", e.Info.ID, "transaction_data_chars", len(wire.GetTransactionData()),
						"has_payment_info", e.SourceWebMsg.GetPaymentInfo() != nil,
						"has_request_key", wire.GetRequestMessageKey() != nil)
				}
				// The primary phone can have the full payment when the linked
				// device receives only a shortened copy. The response re-enters
				// this handler and must pass the same checks before delivery.
				chat, id := e.Info.Chat.ToNonAD(), e.Info.ID
				go func() {
					defer cancelRecovery()
					waitForPaymentRecovery(recoveryCtx, func(requestCtx context.Context, attempt int) {
						_, err := s.conn.SendPeerMessage(requestCtx, s.conn.BuildUnavailableMessageRequest(chat, chat, id))
						if err != nil {
							s.log.Warn("payment recovery request failed", "attempt", attempt, "err", err)
						} else {
							s.log.Info("requested full payment from primary device", "attempt", attempt)
						}
					})
					mu.Lock()
					defer mu.Unlock()
					pending--
					select {
					case recoveryDone <- struct{}{}:
					default:
					}
					if recoveryCtx.Err() != nil || attempted || !time.Now().Before(s.end) {
						return
					}
					if s.assumeUnreadable {
						finish(e, true, "", false)
						return
					}
					s.log.Warn("payment amount unreadable after recovery; not sending the meal image. Check the buyer's chat, or rerun with -assume-unreadable-payment",
						"date", d.Date, "meal", d.Meal, "payment_id", e.Info.ID)
				}()
			}
			if s.diagnostics {
				s.log.Info("buyer message did not trigger delivery", "at", e.Info.Timestamp, "reason", reason.Error())
			}
			return
		}
		finish(e, false, paidAmount, false)
	})
	defer s.conn.RemoveEventHandler(handler)
	if t.listening != nil {
		if err := t.listening(); err != nil {
			return err
		}
	}
	if t.prompt != "" {
		send := true
		if t.prompting != nil {
			if send, err = t.prompting(ctx); err != nil {
				return err
			}
		}
		if !send {
			// It went out before a restart.
			mu.Lock()
			prompted = true
			mu.Unlock()
		}
		if send {
			prompt := t.prompt
			if queued {
				prompt += fmt.Sprintf(". Please pay before %s. If you don't want the meal anymore, reply \"not interested\".", payUntil.In(s.loc).Format("15:04:05"))
			}
			if _, err := s.send(ctx, buyer, &waE2E.Message{Conversation: proto.String(prompt)}); err != nil {
				return fmt.Errorf("offer send failed or is uncertain; inspect chat before retrying: %w", err)
			}
			mu.Lock()
			prompted = true
			mu.Unlock()
			s.log.Info("offer sent to selected buyer")
			if err := s.st.MarkOfferSent(ctx, d, time.Now()); err != nil {
				s.log.Warn("could not record the offer time", "err", err)
			}
		}
	}
	s.log.Info("armed meal delivery; only a new payment message from the selected buyer can trigger it, and payment is assumed, not bank-verified",
		"date", d.Date, "meal", d.Meal, "amount", d.Amount, "ends", s.end.Format(time.RFC3339))
	// waitDelivery covers a delivery already under way when the turn would
	// otherwise end: its outcome decides the turn.
	waitDelivery := func(fallback error) error {
		mu.Lock()
		running := delivering
		mu.Unlock()
		if running {
			return <-done
		}
		return fallback
	}
	select {
	case err := <-done:
		return err
	case <-deadline:
		for {
			mu.Lock()
			expired := pending == 0 && !attempted
			if expired {
				attempted = true
			}
			mu.Unlock()
			if expired {
				return errBuyerTimedOut
			}
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				return waitDelivery(ctx.Err())
			case <-recoveryDone:
			}
		}
	case <-ctx.Done():
		return waitDelivery(ctx.Err())
	}
}

// paidBy names a payment kind for the log.
func paidBy(kind string) string {
	if kind == store.PaidByImage {
		return "image (not verified)"
	}
	return "WhatsApp Pay"
}

// saleJournal records a queued sale's progress. Sell tasks persist it so
// a restart resumes without sending anything twice; deliver-qr keeps it in
// memory.
type saleJournal interface {
	// price is what to ask buyer. It is the seam for negotiation.
	price(ctx context.Context, buyer types.JID) (string, error)
	arm(ctx context.Context, d store.Delivery) (time.Time, error)
	// prompting claims the payment prompt for buyer before it is sent.
	prompting(ctx context.Context, buyer types.JID, amount string, payUntil time.Time) (bool, error)
	turnEnded(ctx context.Context, buyer types.JID, state string) error
	// tellingSold claims the "sorry sold" message before it is sent.
	tellingSold(ctx context.Context, buyer types.JID) (bool, error)
}

// quoteJournal is a journal whose buyers may pay any of several prices the
// bot quoted them, not only the one their turn asks.
type quoteJournal interface {
	accepts(buyer types.JID, agreed string) []string
	reprice(ctx context.Context, d store.Delivery, amount string) (store.Delivery, error)
}

// resumedTurn is a buyer whose turn was under way when run stopped.
type resumedTurn struct {
	buyer    types.JID
	d        store.Delivery
	payUntil time.Time
	// listening runs once the buyer's payments are watched again.
	listening func()
}

// sellToQueue gives each buyer from q a turn, in reply order, until one
// pays, or hands the first buyer over when s.handoff is set. firstWait limits how long to wait for the first reply; zero waits
// until ctx ends. notifyCtx outlives ctx for the "sorry sold" messages.
func (s *sale) sellToQueue(ctx, notifyCtx context.Context, q *offerQueue, j saleJournal, firstWait time.Duration, resume *resumedTurn) error {
	wait := ctx
	if firstWait > 0 && resume == nil {
		var stop context.CancelFunc
		wait, stop = context.WithTimeout(ctx, firstWait)
		defer stop()
	}
	for {
		var buyer types.JID
		var d store.Delivery
		var t turn
		if resume != nil {
			buyer, d = resume.buyer, resume.d
			// The prompt went out before the restart; only listen again.
			t = turn{arm: s.st.ArmDelivery, payUntil: resume.payUntil}
			if qj, ok := j.(quoteJournal); ok {
				t.amounts, t.reprice = qj.accepts(buyer, d.Amount), qj.reprice
			}
			if listening := resume.listening; listening != nil {
				t.listening = func() error { listening(); return nil }
			}
			resume = nil
		} else {
			var err error
			if buyer, err = q.next(wait); err != nil {
				return err
			}
			wait = ctx
			amount, err := j.price(ctx, buyer)
			if err != nil {
				s.log.Warn("not offering the meal to this buyer", "err", err)
				if err := j.turnEnded(ctx, buyer, store.BuyerSkipped); err != nil {
					return err
				}
				continue
			}
			if err := s.checkRegistration(ctx); err != nil {
				return err
			}
			d = s.reservation(buyer, amount)
			if s.handoff != nil {
				if err := s.handoff(ctx, buyer, d, q.closeAndDrain()); err != nil {
					return err
				}
				return errHandedOff
			}
			payUntil := time.Now().Add(2 * time.Minute).Truncate(time.Second)
			if s.end.Before(payUntil) {
				payUntil = s.end
			}
			t = turn{
				arm:      j.arm,
				prompt:   paymentPrompt(amount, s.testImage),
				payUntil: payUntil,
				prompting: func(ctx context.Context) (bool, error) {
					return j.prompting(ctx, buyer, amount, payUntil)
				},
			}
			if qj, ok := j.(quoteJournal); ok {
				t.amounts, t.reprice = qj.accepts(buyer, amount), qj.reprice
			}
		}
		err := s.takeTurn(ctx, buyer, d, t)
		if err == nil {
			if s.sold != nil {
				s.sold()
			}
			if err := j.turnEnded(ctx, buyer, store.BuyerBought); err != nil {
				s.log.Warn("could not record the sale", "err", err)
			}
			waiting := q.closeAndDrain()
			if s.others != nil {
				waiting = append(waiting, s.others()...)
			}
			s.tellSold(notifyCtx, j, waiting)
			return nil
		}
		if !errors.Is(err, errBuyerPassed) && !errors.Is(err, errBuyerTimedOut) {
			return err
		}
		if err := s.st.ReleaseUnpaidDelivery(ctx, d); err != nil {
			return err
		}
		state := store.BuyerTimedOut
		if errors.Is(err, errBuyerPassed) {
			state = store.BuyerPassed
		}
		if err := j.turnEnded(ctx, buyer, state); err != nil {
			return err
		}
		s.log.Info("buyer turn ended; checking queue", "reason", err)
	}
}

// tellSold says "sorry sold" to each buyer the journal lets it claim.
func (s *sale) tellSold(notifyCtx context.Context, j saleJournal, buyers []types.JID) {
	told := map[types.JID]bool{}
	for _, waiting := range buyers {
		if told[waiting] {
			continue
		}
		told[waiting] = true
		if ok, err := j.tellingSold(notifyCtx, waiting); err != nil || !ok {
			continue
		}
		sendCtx, stop := context.WithTimeout(notifyCtx, time.Minute)
		_, notifyErr := s.send(sendCtx, waiting, &waE2E.Message{Conversation: proto.String("sorry sold")})
		stop()
		if notifyErr != nil {
			s.log.Warn("could not notify queued buyer that meal sold", "err", notifyErr)
		}
	}
}

// paymentPrompt asks a buyer to pay amount. testImage says the QR is the
// embedded placeholder.
func paymentPrompt(amount string, testImage bool) string {
	price := "₹" + strings.TrimSuffix(amount, ".00")
	if testImage {
		return fmt.Sprintf("pay %s here via WhatsApp Pay for the test QR", price)
	}
	return fmt.Sprintf("pay %s here via WhatsApp Pay, I'll send the QR", price)
}

// memoryJournal is deliver-qr's journal: one fixed price, nothing saved
// beyond the delivery record.
type memoryJournal struct {
	amount string
	st     *store.Store
}

func (j memoryJournal) price(context.Context, types.JID) (string, error) { return j.amount, nil }
func (j memoryJournal) arm(ctx context.Context, d store.Delivery) (time.Time, error) {
	return j.st.ArmNewDelivery(ctx, d)
}
func (memoryJournal) prompting(context.Context, types.JID, string, time.Time) (bool, error) {
	return true, nil
}
func (memoryJournal) turnEnded(context.Context, types.JID, string) error   { return nil }
func (memoryJournal) tellingSold(context.Context, types.JID) (bool, error) { return true, nil }
