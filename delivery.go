package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/meals"
	"messwa/internal/portal"
	"messwa/internal/store"
	"messwa/internal/wa"
)

//go:embed assets/qr-placeholder.png
var placeholderQR []byte

var errBuyerPassed = errors.New("buyer passed on the meal")
var errBuyerTimedOut = errors.New("buyer payment window expired")

func deliverQR(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("deliver-qr", flag.ContinueOnError)
	date := fs.String("date", "", "meal date, YYYY-MM-DD in the configured timezone")
	meal := fs.String("meal", "", "configured meal name")
	peer := fs.String("peer", "", "reserved buyer's full WhatsApp JID")
	group := fs.String("group", "", "post an offer to this exact group name or JID and wait for a private reply")
	amount := fs.String("amount", "", "agreed INR amount, e.g. 60.00")
	qr := fs.String("qr", "", "PNG or JPEG image to send (defaults to an embedded, labelled test image)")
	portalQR := fs.Bool("portal-qr", false, "fetch the current QR from the portal immediately before delivery, with -user's mess API key")
	user := fs.String("user", "", "with -portal-qr, whose mess API key to use; defaults to the first admin")
	offer := fs.String("offer-text", "", "group post with -group, or optional private offer with -peer")
	diagnostics := fs.Bool("diagnostics", false, "log delivery checks for the selected buyer without message contents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if (*peer == "") == (*group == "") {
		return errors.New("choose either -peer or -group")
	}
	if *group != "" && !*portalQR && strings.TrimSpace(*offer) == "" {
		return errors.New("-group requires -offer-text or -portal-qr")
	}
	if *portalQR && *qr != "" {
		return errors.New("choose either -portal-qr or -qr")
	}
	day, err := time.ParseInLocation("2006-01-02", *date, cfg.Location())
	if err != nil {
		return errors.New("-date must be YYYY-MM-DD")
	}
	var slot meals.Slot
	for _, w := range meals.New(cfg).Windows(day) {
		if w.Meal == *meal {
			slot = w
		}
	}
	if slot.Meal == "" {
		return errors.New("-meal must name a configured meal")
	}
	if !time.Now().Before(slot.End) {
		return errors.New("meal has already ended")
	}
	var jid types.JID
	if *peer != "" {
		jid, err = types.ParseJID(*peer)
		if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
			return errors.New("-peer must be a personal WhatsApp JID")
		}
		jid = jid.ToNonAD()
	}
	if !decimalAmount.MatchString(*amount) || strings.Trim(*amount, "0.") == "" {
		return errors.New("-amount must be a positive INR value with two decimal places")
	}
	// Normalize without floats so e.g. 060.00 matches the received 60.00.
	parts := strings.Split(*amount, ".")
	parts[0] = strings.TrimLeft(parts[0], "0")
	if parts[0] == "" {
		parts[0] = "0"
	}
	*amount = parts[0] + "." + parts[1]
	data, mime, width, height, err := readDeliveryImage(*qr)
	if err != nil {
		return err
	}
	imageHash := fmt.Sprintf("%x", sha256.Sum256(data))
	var portalClient *portal.Client
	var accountHash, mess, diet, portalMess string
	mealLabel := *meal
	if *portalQR {
		portalClient, err = userPortal(ctx, cfg, *user)
		if err != nil {
			return err
		}
		current, err := portalClient.QRForMeal(ctx, *date, *meal, "")
		if err != nil {
			return err
		}
		accountHash = current.AccountHash
		portalMess = current.Registration.Mess
		mess, diet = current.Registration.MessDiet()
		if diet == "" && cfg.VegOnly(mess) {
			diet = "veg"
		}
		mealLabel = portalMealLabel(mess, diet, *meal)
		// Bind restarts to the account, not the token that rotates daily.
		imageHash = "portal:" + accountHash
	}
	c, err := wa.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	if !c.LoggedIn() {
		return errors.New("not linked; run messwa login first")
	}
	if *peer != "" && (jid == c.WA.Store.GetJID().ToNonAD() || jid == c.WA.Store.GetLID().ToNonAD()) {
		return errors.New("buyer cannot be your own account")
	}
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return err
	}
	defer st.Close()
	commandCtx := ctx
	ctx, cancel := context.WithDeadline(ctx, slot.End)
	defer cancel()
	defer c.WA.Disconnect()
	paymentPrompt := *offer
	armDelivery := st.ArmDelivery
	var queue *offerQueue
	if *group != "" {
		exists, err := st.DeliveryExists(ctx, *date, *meal)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("meal already has a delivery reservation")
		}
		if err := c.WA.ConnectContext(ctx); err != nil {
			return err
		}
		if !c.WA.WaitForConnection(20 * time.Second) {
			return errors.New("WhatsApp connection not ready")
		}
		groupPost := *offer
		price := "₹" + strings.TrimSuffix(*amount, ".00")
		if strings.TrimSpace(groupPost) == "" {
			groupPost = fmt.Sprintf("sell %s %s", mealLabel, price)
			if *date != time.Now().In(cfg.Location()).Format("2006-01-02") {
				groupPost += " (" + day.Format("2 Jan 2006") + ")"
			}
		}
		replies, closeOffer, err := openGroupOffer(ctx, c, log, *group, groupPost)
		if err != nil {
			return err
		}
		defer closeOffer()
		queue = replies
		paymentPrompt = fmt.Sprintf("pay %s here via WhatsApp Pay, I'll send the QR", price)
		if *qr == "" && !*portalQR {
			paymentPrompt = fmt.Sprintf("pay %s here via WhatsApp Pay for the test QR", price)
		}
		armDelivery = st.ArmNewDelivery
	}
	tryBuyer := func(ctx context.Context, jid types.JID, d store.Delivery) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		sendQR := func(recipient types.JID, id string) error {
			if portalClient != nil {
				current, err := portalClient.QRForMeal(ctx, d.Date, d.Meal, accountHash)
				if err != nil {
					return err
				}
				if *group != "" && current.Registration.Mess != portalMess {
					return errors.New("portal registration changed during the sale")
				}
				data, mime, width, height, err = deliveryImageBytes(current.PNG)
				if err != nil {
					return err
				}
			}
			up, err := c.WA.Upload(ctx, data, whatsmeow.MediaImage)
			if err != nil {
				return err
			}
			_, err = c.WA.SendMessage(ctx, recipient, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
				Caption: proto.String(fmt.Sprintf("%s %s", d.Date, d.Meal)), Mimetype: proto.String(mime), Width: proto.Uint32(uint32(width)), Height: proto.Uint32(uint32(height)),
				URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength,
			}}, whatsmeow.SendRequestExtra{ID: id})
			return err
		}
		armedAt, err := armDelivery(ctx, d)
		if err != nil {
			return err
		}
		// WhatsApp event timestamps have second precision. Start at the next second
		// so messages from just before the first arming cannot qualify.
		armedAt = armedAt.Add(time.Second)
		var payUntil time.Time
		var deadline <-chan time.Time
		if queue != nil {
			payUntil = time.Now().Add(2 * time.Minute).Truncate(time.Second)
			if slot.End.Before(payUntil) {
				payUntil = slot.End
			}
			timer := time.NewTimer(time.Until(payUntil))
			defer timer.Stop()
			deadline = timer.C
		}
		done := make(chan error, 1)
		recoveryDone := make(chan struct{}, 1)
		pending := 0
		var mu sync.Mutex
		attempted := false
		requestedPayments := map[string]context.CancelFunc{}
		finish := func(e *events.Message, amountMissing bool) {
			attempted = true
			for _, stop := range requestedPayments {
				stop()
			}
			if amountMissing {
				log.Warn("payment amount unavailable after recovery; assuming agreed price",
					"date", d.Date, "meal", d.Meal, "amount_assumed", d.Amount, "payment_id", e.Info.ID)
			}
			id := c.WA.GenerateMessageID()
			paid := store.Payment{ID: e.Info.ID, At: e.Info.Timestamp, BuyerName: e.Info.PushName}
			log.Info("payment assumed; sending meal image", "date", d.Date, "meal", d.Meal, "amount", d.Amount)
			err := deliverOnce(ctx, st, d, paid, id, func() error { return sendQR(e.Info.Chat, id) })
			if err != nil {
				done <- err
				return
			}
			log.Info("meal image sent", "date", d.Date, "meal", d.Meal, "message_id", id)
			done <- nil
		}
		matchesBuyer := func(info types.MessageInfo) bool {
			if info.IsFromMe || info.IsGroup {
				return false
			}
			if info.Chat.ToNonAD() == jid || info.SenderAlt.ToNonAD() == jid {
				return true
			}
			if info.Chat.Server == types.HiddenUserServer && jid.Server == types.DefaultUserServer {
				pn, err := c.WA.Store.LIDs.GetPNForLID(ctx, info.Chat.ToNonAD())
				return err == nil && pn.ToNonAD() == jid
			}
			return false
		}
		handler := c.WA.AddEventHandler(func(evt any) {
			if *diagnostics {
				switch e := evt.(type) {
				case *events.Connected:
					log.Info("WhatsApp connected")
				case *events.Disconnected:
					log.Warn("WhatsApp disconnected")
				case *events.LoggedOut:
					log.Warn("WhatsApp session logged out")
				case *events.UndecryptableMessage:
					if matchesBuyer(e.Info) {
						log.Warn("could not decrypt buyer message", "at", e.Info.Timestamp, "unavailable", e.IsUnavailable)
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
			if !payUntil.IsZero() && (!e.Info.Timestamp.Before(payUntil) || (stopRecovery == nil && !time.Now().Before(payUntil))) {
				return
			}
			if queue != nil && pending == 0 && !e.Info.Timestamp.Before(armedAt) && buyerPassed(e.Message) {
				attempted = true
				done <- errBuyerPassed
				return
			}
			reason := deliveryRejection(e, d.Amount, armedAt, slot.End, time.Now())
			if !errors.Is(reason, errTruncatedPayment) && stopRecovery != nil {
				stopRecovery()
			}
			if reason != nil {
				if errors.Is(reason, errTruncatedPayment) && stopRecovery == nil {
					recoveryCtx, cancelRecovery := context.WithCancel(ctx)
					requestedPayments[e.Info.ID] = cancelRecovery
					pending++
					if *diagnostics {
						wire := e.Message.GetSendPaymentMessage()
						log.Info("incomplete payment fields", "message_id", e.Info.ID, "transaction_data_chars", len(wire.GetTransactionData()),
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
							_, err := c.WA.SendPeerMessage(requestCtx, c.WA.BuildUnavailableMessageRequest(chat, chat, id))
							if err != nil {
								log.Warn("payment recovery request failed", "attempt", attempt, "err", err)
							} else {
								log.Info("requested full payment from primary device", "attempt", attempt)
							}
						})
						mu.Lock()
						defer mu.Unlock()
						pending--
						select {
						case recoveryDone <- struct{}{}:
						default:
						}
						if recoveryCtx.Err() == nil && !attempted && time.Now().Before(slot.End) {
							finish(e, true)
						}
					}()
				}
				if *diagnostics {
					log.Info("buyer message did not trigger delivery", "at", e.Info.Timestamp, "reason", reason.Error())
				}
				return
			}
			finish(e, false)
		})
		defer c.WA.RemoveEventHandler(handler)
		if *group == "" {
			if err := c.WA.Connect(); err != nil {
				return err
			}
		}
		if *diagnostics && !c.WA.WaitForConnection(20*time.Second) {
			return errors.New("WhatsApp connection not ready")
		}
		if paymentPrompt != "" {
			if !c.WA.WaitForConnection(20 * time.Second) {
				return errors.New("WhatsApp connection not ready; offer was not sent")
			}
			prompt := paymentPrompt
			if queue != nil {
				prompt += fmt.Sprintf(". Please pay before %s. If you don't want the meal anymore, reply \"not interested\".", payUntil.In(cfg.Location()).Format("15:04:05"))
			}
			if _, err := c.WA.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(prompt)}); err != nil {
				return fmt.Errorf("offer send failed or is uncertain; inspect chat before retrying: %w", err)
			}
			log.Info("offer sent to selected buyer")
			if err := st.MarkOfferSent(ctx, d, time.Now()); err != nil {
				log.Warn("could not record the offer time", "err", err)
			}
		}
		fmt.Fprintf(os.Stderr, "Armed %s %s for INR %s, ending %s. Only a new payment message from the selected buyer can trigger delivery. Payment is assumed, not bank-verified.\n", d.Date, d.Meal, d.Amount, slot.End.Format(time.RFC3339))
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
					return ctx.Err()
				case <-recoveryDone:
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	newReservation := func(peer types.JID) store.Delivery {
		return store.Delivery{Date: *date, Meal: *meal, Peer: peer.String(), Amount: *amount, ImageHash: imageHash, ArmedAt: time.Now(), Mess: mess, Diet: diet}
	}
	if queue == nil {
		return tryBuyer(ctx, jid, newReservation(jid))
	}
	wait, stopWaiting := context.WithTimeout(ctx, 10*time.Minute)
	defer stopWaiting()
	for {
		peer, err := queue.next(wait)
		if err != nil {
			return err
		}
		stopWaiting()
		wait = ctx
		if portalClient != nil {
			reg, err := portalClient.RequireRegistration(ctx, *date, *meal)
			if err != nil {
				return err
			}
			if reg.Mess != portalMess {
				return errors.New("portal registration changed since the group offer")
			}
		}
		d := newReservation(peer)
		err = tryBuyer(ctx, peer, d)
		if err == nil {
			for _, waiting := range queue.closeAndDrain() {
				notifyCtx, stop := context.WithTimeout(commandCtx, 5*time.Second)
				_, notifyErr := c.WA.SendMessage(notifyCtx, waiting, &waE2E.Message{Conversation: proto.String("sorry sold")})
				stop()
				if notifyErr != nil {
					log.Warn("could not notify queued buyer that meal sold", "err", notifyErr)
				}
			}
			return nil
		}
		if !errors.Is(err, errBuyerPassed) && !errors.Is(err, errBuyerTimedOut) {
			return err
		}
		if err := st.ReleaseUnpaidDelivery(ctx, d); err != nil {
			return err
		}
		log.Info("buyer turn ended; checking queue", "reason", err)
	}
}

func buyerPassed(message *waE2E.Message) bool {
	text := message.GetConversation()
	if text == "" {
		text = message.GetExtendedTextMessage().GetText()
	}
	switch strings.Trim(strings.ToLower(strings.TrimSpace(text)), ".!?") {
	case "pass", "cancel", "no thanks", "not interested":
		return true
	default:
		return false
	}
}

func portalMealLabel(mess, diet, meal string) string {
	if diet == "nonveg" {
		diet = "non-veg"
	}
	return strings.Join(strings.Fields(mess+" "+diet+" "+meal), " ")
}

func waitForPaymentRecovery(ctx context.Context, request func(context.Context, int)) {
	requestCtx, stopRequests := context.WithTimeout(ctx, 40*time.Second)
	defer stopRequests()
	for attempt := 1; attempt <= 6 && requestCtx.Err() == nil; attempt++ {
		request(requestCtx, attempt)
		select {
		case <-requestCtx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func deliverOnce(ctx context.Context, st *store.Store, d store.Delivery, paid store.Payment, messageID string, send func() error) error {
	claimed, err := st.ClaimDelivery(ctx, d, paid, messageID)
	if err != nil {
		return err
	}
	if !claimed {
		return errors.New("meal already claimed; no duplicate delivery attempted")
	}
	if err := send(); err != nil {
		return fmt.Errorf("delivery attempt failed or is uncertain; no automatic retry: %w", err)
	}
	if err := st.FinishDelivery(ctx, d, messageID); err != nil {
		return fmt.Errorf("image sent but completion could not be recorded: %w", err)
	}
	return nil
}

func qualifiesForDelivery(e *events.Message, amount string, armedAt, endsAt, now time.Time) bool {
	return deliveryRejection(e, amount, armedAt, endsAt, now) == nil
}

func deliveryRejection(e *events.Message, amount string, armedAt, endsAt, now time.Time) error {
	if e == nil || e.Info.IsFromMe || e.Info.IsGroup || e.Info.ID == "" {
		return errors.New("not an incoming DM with a message ID")
	}
	if e.Info.Chat.Server != types.DefaultUserServer && e.Info.Chat.Server != types.HiddenUserServer {
		return errors.New("not a personal chat")
	}
	if e.Info.Timestamp.Before(armedAt) || !e.Info.Timestamp.Before(endsAt) || !now.Before(endsAt) || e.Info.Timestamp.After(now.Add(5*time.Second)) {
		return errors.New("message time outside the delivery window")
	}
	payment := e.Message.GetSendPaymentMessage()
	if payment == nil {
		return errors.New("no sendPaymentMessage field")
	}
	info := e.SourceWebMsg.GetPaymentInfo()
	status, txnStatus := info.GetStatus(), info.GetTxnStatus()
	if (status != waWeb.PaymentInfo_UNKNOWN_STATUS && status != waWeb.PaymentInfo_COMPLETE) ||
		(txnStatus != waWeb.PaymentInfo_UNKNOWN && txnStatus != waWeb.PaymentInfo_SUCCESS && txnStatus != waWeb.PaymentInfo_COMPLETED) {
		return errors.New("WhatsApp reports that payment is not complete")
	}
	p, err := decodePaymentPayload(payment.GetTransactionData())
	if p != nil && (p.Timestamp.Before(armedAt) || !p.Timestamp.Before(endsAt) || p.Timestamp.After(now.Add(5*time.Second))) {
		return errors.New("transaction time outside the delivery window")
	}
	if errors.Is(err, errTruncatedPayment) && info != nil {
		currency := info.GetCurrency()
		if currency != "" && currency != "INR" {
			return errors.New("payment currency does not match")
		}
		timestamp := time.Unix(int64(info.GetTransactionTimestamp()), 0)
		if info.TransactionTimestamp != nil && (timestamp.Before(armedAt) || !timestamp.Before(endsAt) || timestamp.After(now.Add(5*time.Second))) {
			return errors.New("transaction time outside the delivery window")
		}
		if info.Amount1000 != nil {
			thousandths := info.GetAmount1000()
			reported := fmt.Sprintf("%d.%02d", thousandths/1000, thousandths%1000/10)
			if thousandths%10 != 0 || reported != amount {
				return errors.New("payment amount does not match")
			}
			if status == waWeb.PaymentInfo_COMPLETE && txnStatus == waWeb.PaymentInfo_SUCCESS && currency != "" && info.TransactionTimestamp != nil {
				p = &paymentPayloadSummary{
					Currency:  currency,
					Amount:    reported,
					Timestamp: timestamp,
				}
				err = nil
			}
		} else if primary := info.GetPrimaryAmount(); primary != nil && primary.Value != nil {
			return errors.New("unsupported payment amount format")
		}
	}
	if err != nil {
		return err
	}
	if p.Currency != "INR" || p.Amount != amount {
		return errors.New("payment amount or currency does not match")
	}
	return nil
}

func readDeliveryImage(path string) ([]byte, string, int, int, error) {
	data := placeholderQR
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", 0, 0, err
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, 5*1024*1024+1))
		if err != nil {
			return nil, "", 0, 0, err
		}
	}
	return deliveryImageBytes(data)
}

func deliveryImageBytes(data []byte) ([]byte, string, int, int, error) {
	if len(data) > 5*1024*1024 {
		return nil, "", 0, 0, errors.New("image exceeds 5 MiB")
	}
	info, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || info.Width <= 0 || info.Height <= 0 || info.Width > 8192 || info.Height > 8192 {
		return nil, "", 0, 0, errors.New("image must be a valid PNG or JPEG, up to 8192 pixels per side")
	}
	return data, "image/" + format, info.Width, info.Height, nil
}
