package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/store"
)

// This observer never feeds messages or failures back into sale decisions.
func (m *sellManager) observeConversation(evt any) {
	e, ok := evt.(*events.Message)
	if !ok || e.Info.IsGroup {
		return
	}
	info := e.Info
	if info.IsFromMe {
		info.SenderAlt = info.RecipientAlt
	}
	peer := buyerPeer(m.base, m.conn, info)
	alt := e.Info.SenderAlt.String()
	if e.Info.IsFromMe {
		alt = e.Info.RecipientAlt.String()
	}
	m.recordConversation(peer.String(), alt, e.Info.ID, e.Info.Timestamp, e.Info.IsFromMe, e.Message)
}

// conversationRetry is how long the recorder waits before each further
// lookup of a message that matched no sale. A new buyer's first DM races
// the offer handler admitting them, so it can miss the first lookup.
var conversationRetry = []time.Duration{2 * time.Second, 5 * time.Second}

// mediaSlots bounds attachment downloads at once, so a burst of buyer
// media can't crowd out live selling.
var mediaSlots = make(chan struct{}, 2)

func (m *sellManager) recordConversation(peer, alt, id string, at time.Time, fromMe bool, msg *waE2E.Message) {
	if msg == nil || id == "" {
		return
	}
	// Clone before leaving the WhatsApp callback; callers may reuse messages.
	copyMsg := proto.Clone(msg).(*waE2E.Message)
	go func() {
		ctx, cancel := context.WithTimeout(m.base, 30*time.Second)
		defer cancel()
		quote := copyMsg.GetExtendedTextMessage().GetContextInfo().GetStanzaID()
		if quote == "" {
			quote = copyMsg.GetImageMessage().GetContextInfo().GetStanzaID()
		}
		task, canonical, err := m.st.ConversationTask(ctx, peer, alt, quote, at.Unix())
		for _, wait := range conversationRetry {
			if err != nil || task != 0 || fromMe {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			task, canonical, err = m.st.ConversationTask(ctx, peer, alt, quote, at.Unix())
		}
		if err != nil || task == 0 {
			return
		}
		item := store.SaleMessage{ID: id, At: at.Unix(), FromMe: fromMe, Text: messageText(copyMsg), Kind: "text"}
		mime := ""
		var size uint64
		if im := copyMsg.GetImageMessage(); im != nil {
			item.Kind = "image"
			item.Text = im.GetCaption()
			mime = im.GetMimetype()
			size = im.GetFileLength()
			if !fromMe {
				item.Checking = "Image or screenshot has not been verified."
			}
		}
		if a := copyMsg.GetAudioMessage(); a != nil {
			item.Kind = "audio"
			mime = a.GetMimetype()
			size = a.GetFileLength()
		}
		if a := copyMsg.GetVideoMessage(); a != nil {
			item.Kind = "video"
			item.Text = a.GetCaption()
			mime = a.GetMimetype()
			size = a.GetFileLength()
		}
		if a := copyMsg.GetDocumentMessage(); a != nil {
			item.Kind = "document"
			item.Text = a.GetFileName()
			mime = a.GetMimetype()
			size = a.GetFileLength()
		}
		if copyMsg.GetSendPaymentMessage() != nil {
			item.Kind = "payment"
			item.Checking = "WhatsApp payment payload does not confirm bank settlement."
			item.Text = conversationPaymentText(copyMsg)
		}
		if item.Text == "" && item.Kind == "text" {
			item.Kind = "attachment"
			item.Text = "Message type not available in this view."
		}
		if err := m.st.AddSaleMessage(ctx, task, canonical, item); err != nil {
			m.log.Warn("conversation recording failed", "err", err)
			return
		}
		if mime != "" {
			if d, ok := m.conn.(interface {
				DownloadConversationMedia(context.Context, *waE2E.Message) ([]byte, error)
			}); ok {
				if size <= 10<<20 {
					select {
					case mediaSlots <- struct{}{}:
						defer func() { <-mediaSlots }()
					case <-ctx.Done():
						return
					}
					data, err := d.DownloadConversationMedia(ctx, copyMsg)
					if err == nil && len(data) <= 10<<20 {
						_ = m.st.SetSaleMessageMedia(ctx, task, canonical, id, mime, data)
					}
				}
			}
		}
	}()
}

// conversationPaymentText is "Paid INR 65.00", the amount decoded from
// the payload, and the buyer's note on a second line. The dashboard draws
// it as a WhatsApp payment card. The raw payload stays out: it is opaque
// to the seller and carries transaction data they have no use for.
func conversationPaymentText(msg *waE2E.Message) string {
	pay := msg.GetSendPaymentMessage()
	text := "Amount unavailable."
	p, err := decodePaymentPayload(pay.GetTransactionData())
	if err == nil && p != nil && p.Amount != "" {
		text = fmt.Sprintf("Paid %s %s", p.Currency, p.Amount)
	}
	// The note is usually in noteMessage; Android UPI payments may carry
	// it only inside the payload, as interopNote.
	note := strings.TrimSpace(messageText(pay.GetNoteMessage()))
	if note == "" && p != nil && p.Note != nil {
		note = strings.TrimSpace(*p.Note)
	}
	if note != "" {
		text += "\nNote: " + note
	}
	return text
}
