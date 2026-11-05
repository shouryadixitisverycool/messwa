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
	ctx, cancel := context.WithDeadline(ctx, slot.End)
	defer cancel()
	defer c.WA.Disconnect()
	paymentPrompt := *offer
	armDelivery := st.ArmDelivery
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
		buyer, err := postGroupOffer(ctx, c, log, *group, groupPost, 10*time.Minute)
		if err != nil {
			return err
		}
		if portalClient != nil {
			reg, err := portalClient.RequireRegistration(ctx, *date, *meal)
			if err != nil {
				return err
			}
			if reg.Mess != portalMess {
				return errors.New("portal registration changed since the group offer")
			}
		}
		jid = buyer.ToNonAD()
		if jid.Server == types.HiddenUserServer {
			if pn, err := c.WA.Store.LIDs.GetPNForLID(ctx, jid); err == nil && pn.User != "" {
				jid = pn.ToNonAD()
			}
		}
		paymentPrompt = fmt.Sprintf("pay %s here via WhatsApp Pay, I'll send the QR", price)
		if *qr == "" && !*portalQR {
			paymentPrompt = fmt.Sprintf("pay %s here via WhatsApp Pay for the test QR", price)
		}
		armDelivery = st.ArmNewDelivery
	}
	d := store.Delivery{Date: *date, Meal: *meal, Peer: jid.String(), Amount: *amount, ImageHash: imageHash, ArmedAt: time.Now(), Mess: mess, Diet: diet}
	armedAt, err := armDelivery(ctx, d)
	if err != nil {
		return err
	}
	// WhatsApp event timestamps have second precision. Start at the next second
	// so messages from just before the first arming cannot qualify.
	armedAt = armedAt.Add(time.Second)
	done := make(chan error, 1)
	var mu sync.Mutex
	attempted := false
	requestedPayments := map[string]bool{}
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
	c.WA.AddEventHandler(func(evt any) {
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
		if reason := deliveryRejection(e, d.Amount, armedAt, slot.End, time.Now()); reason != nil {
			if errors.Is(reason, errTruncatedPayment) && !requestedPayments[e.Info.ID] {
				requestedPayments[e.Info.ID] = true
				// The primary phone can have the full payment when the linked
				// device receives only a shortened copy. The response re-enters
				// this handler and must pass the same checks before delivery.
				chat, id := e.Info.Chat.ToNonAD(), e.Info.ID
				go func() {
					_, err := c.WA.SendPeerMessage(ctx, c.WA.BuildUnavailableMessageRequest(chat, chat, id))
					if err != nil {
						log.Warn("could not recover payment from primary device", "err", err)
						return
					}
					log.Info("requested full payment from primary device")
				}()
			}
			if *diagnostics {
				log.Info("buyer message did not trigger delivery", "at", e.Info.Timestamp, "reason", reason.Error())
			}
			return
		}
		attempted = true
		id := c.WA.GenerateMessageID()
		paid := store.Payment{ID: e.Info.ID, At: e.Info.Timestamp, BuyerName: e.Info.PushName}
		err := deliverOnce(ctx, st, d, paid, id, func() error {
			if portalClient != nil {
				current, err := portalClient.QRForMeal(ctx, d.Date, d.Meal, accountHash)
				if err != nil {
					return err
				}
				if *group != "" && current.Registration.Mess != portalMess {
					return errors.New("portal registration changed since the group offer")
				}
				data, mime, width, height, err = deliveryImageBytes(current.PNG)
				if err != nil {
					return err
				}
			}
			log.Info("payment assumed; sending meal image", "date", d.Date, "meal", d.Meal, "amount", d.Amount)
			up, err := c.WA.Upload(ctx, data, whatsmeow.MediaImage)
			if err != nil {
				return err
			}
			_, err = c.WA.SendMessage(ctx, e.Info.Chat, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
				Caption: proto.String(fmt.Sprintf("%s %s", d.Date, d.Meal)), Mimetype: proto.String(mime), Width: proto.Uint32(uint32(width)), Height: proto.Uint32(uint32(height)),
				URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength,
			}}, whatsmeow.SendRequestExtra{ID: id})
			return err
		})
		if err != nil {
			done <- err
			return
		}
		log.Info("meal image sent", "date", d.Date, "meal", d.Meal, "message_id", id)
		done <- nil
	})
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
		if _, err := c.WA.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(paymentPrompt)}); err != nil {
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
	case <-ctx.Done():
		return ctx.Err()
	}
}

func portalMealLabel(mess, diet, meal string) string {
	if diet == "nonveg" {
		diet = "non-veg"
	}
	return strings.Join(strings.Fields(mess+" "+diet+" "+meal), " ")
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
	p, err := decodePaymentPayload(payment.GetTransactionData())
	if err != nil {
		return err
	}
	if p.Currency != "INR" || p.Amount != amount {
		return errors.New("payment amount or currency does not match")
	}
	if p.Timestamp.Before(armedAt) || !p.Timestamp.Before(endsAt) || p.Timestamp.After(now.Add(5*time.Second)) {
		return errors.New("transaction time outside the delivery window")
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
