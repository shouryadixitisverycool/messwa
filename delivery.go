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
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/config"
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
	assumeUnreadable := fs.Bool("assume-unreadable-payment", false, "send the QR when a payment's amount stays unreadable after recovery, assuming the agreed price")
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
	slot, err := findSlot(cfg, *date, *meal)
	if err != nil {
		return errors.New("-" + err.Error())
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
	if *amount, err = normalizeAmount(*amount); err != nil {
		return errors.New("-amount must be a positive INR value with two decimal places")
	}
	data, _, _, _, err := readDeliveryImage(*qr)
	if err != nil {
		return err
	}
	s := &sale{
		log: log, loc: cfg.Location(), date: *date, meal: *meal, end: slot.End, label: *meal,
		image: data, testImage: *qr == "", imageHash: fmt.Sprintf("%x", sha256.Sum256(data)),
		diagnostics: *diagnostics, assumeUnreadable: *assumeUnreadable,
	}
	if *portalQR {
		portalClient, err := userPortal(ctx, cfg, *user)
		if err != nil {
			return err
		}
		s.image, s.testImage = nil, false
		if err := s.usePortal(ctx, portalClient, cfg.VegOnly); err != nil {
			return err
		}
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
	s.st, s.conn = st, waConn{c.WA}
	s.send, s.sendPaid = c.WA.SendMessage, c.WA.SendMessage
	commandCtx := ctx
	ctx, cancel := context.WithDeadline(ctx, slot.End)
	defer cancel()
	defer c.WA.Disconnect()
	if *peer != "" {
		return s.takeTurn(ctx, jid, s.reservation(jid, *amount), turn{
			arm:    st.ArmDelivery,
			prompt: *offer,
			listening: func() error {
				if err := c.WA.Connect(); err != nil {
					return err
				}
				if (*diagnostics || *offer != "") && !c.WA.WaitForConnection(20*time.Second) {
					return errors.New("WhatsApp connection not ready; offer was not sent")
				}
				return nil
			},
		})
	}
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
	if strings.TrimSpace(groupPost) == "" {
		groupPost = offerText(cfg, s.label, *date, *amount, true)
	}
	queue, closeOffer, err := openGroupOffer(ctx, s.conn, s.send, log, *group, groupPost)
	if err != nil {
		return err
	}
	defer closeOffer()
	return s.sellToQueue(ctx, commandCtx, queue, memoryJournal{amount: *amount, st: st}, 10*time.Minute, nil)
}

// normalizeAmount checks an INR amount with two decimal places and drops
// leading zeros without floats, so 060.00 matches the received 60.00.
func normalizeAmount(amount string) (string, error) {
	if !decimalAmount.MatchString(amount) || strings.Trim(amount, "0.") == "" {
		return "", errors.New("not a positive INR amount with two decimal places")
	}
	parts := strings.Split(amount, ".")
	parts[0] = strings.TrimLeft(parts[0], "0")
	if parts[0] == "" {
		parts[0] = "0"
	}
	return parts[0] + "." + parts[1], nil
}

// offerText is the group post for a meal: "sell bakul non-veg lunch ₹60",
// with the date when it isn't today. withPrice false leaves the price out,
// so buyers ask for it in a DM.
func offerText(cfg *config.Config, label, date, amount string, withPrice bool) string {
	text := "sell " + label
	if withPrice {
		text += " ₹" + strings.TrimSuffix(amount, ".00")
	}
	if date != time.Now().In(cfg.Location()).Format("2006-01-02") {
		if day, err := time.ParseInLocation("2006-01-02", date, cfg.Location()); err == nil {
			text += " (" + day.Format("2 Jan 2006") + ")"
		}
	}
	return text
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
