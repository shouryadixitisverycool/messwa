// Package wa owns the WhatsApp connection. It converts whatsmeow events into
// ingest calls and drops everything outside the allowed chats on arrival.
//
// This client never sends read receipts or presence, so your phone keeps
// showing messages as unread and notifications behave as before.
package wa

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"messwa/internal/config"
	"messwa/internal/ingest"
	mstore "messwa/internal/store"
)

type Client struct {
	WA  *whatsmeow.Client
	cfg *config.Config
	log *slog.Logger
}

func Open(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Client, error) {
	store.SetOSInfo("messwa", [3]uint32{0, 1, 0})
	container, err := sqlstore.New(ctx, "sqlite", "file:"+cfg.SessionDB()+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", waLog.Noop)
	if err != nil {
		return nil, fmt.Errorf("session store: %w", err)
	}
	os.Chmod(cfg.SessionDB(), 0o600) // holds the device's keys
	dev, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{WA: whatsmeow.NewClient(dev, waLog.Noop), cfg: cfg, log: log}, nil
}

func (c *Client) LoggedIn() bool { return c.WA.Store.ID != nil }

// Self returns the linked account the way ingest names a sender: phone-number
// JID first, LID second, plus its push name.
func (c *Client) Self(ctx context.Context) (jid, alt, name string) {
	jid, alt = c.identity(ctx, c.WA.Store.GetJID(), c.WA.Store.GetLID())
	return jid, alt, c.WA.Store.PushName
}

// Login links this program as a companion device, by QR code or, when
// phone is given (country code + number, digits only), by pairing code.
// It returns once paired and leaves the client connected, because the
// one-time history sync arrives in the minutes after pairing.
func (c *Client) Login(ctx context.Context, phone string) error {
	if c.LoggedIn() {
		return fmt.Errorf("already linked as %s; delete %s to relink", c.WA.Store.ID, c.cfg.SessionDB())
	}
	done := make(chan struct{})
	c.WA.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.PairSuccess); ok {
			close(done)
		}
	})
	if phone == "" {
		qr, err := c.WA.GetQRChannel(ctx)
		if err != nil {
			return err
		}
		if err := c.WA.Connect(); err != nil {
			return err
		}
		for item := range qr {
			switch item.Event {
			case "code":
				fmt.Println("\nWhatsApp > Settings > Linked devices > Link a device, then scan:")
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
			case "success":
				fmt.Println("Linked.")
			default:
				if item.Error != nil {
					return item.Error
				}
				if item.Event != "success" {
					return fmt.Errorf("pairing: %s", item.Event)
				}
			}
		}
	} else {
		if err := c.WA.Connect(); err != nil {
			return err
		}
		code, err := c.WA.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		if err != nil {
			return err
		}
		fmt.Printf("\n    Code: %s\n\n", code)
		fmt.Println("On your phone: WhatsApp > Settings > Linked devices > Link a device >")
		fmt.Println("\"Link with phone number instead\", then type the code. It expires in a few minutes.")
		fmt.Println("WhatsApp may also show a notification you can tap to enter it.")
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type GroupSummary struct {
	JID, Name    string
	Participants int
}

func (c *Client) Groups(ctx context.Context) ([]GroupSummary, error) {
	gs, err := c.WA.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GroupSummary, 0, len(gs))
	for _, g := range gs {
		out = append(out, GroupSummary{g.JID.String(), g.Name, len(g.Participants)})
	}
	return out, nil
}

// Saved reports whether jid (or its alternate) is in your address book, and
// the best display name for it.
func (c *Client) Saved(ctx context.Context, jids ...string) (bool, string) {
	parsed := make([]types.JID, 0, len(jids))
	for _, s := range jids {
		if s == "" {
			continue
		}
		if j, err := types.ParseJID(s); err == nil {
			parsed = append(parsed, j)
		}
	}
	return SavedContact(ctx, c.WA.Store, parsed...)
}

// SavedContact reports whether any of jids is in your address book, and the
// best display name for it. Each JID is also looked up under the phone
// number or LID WhatsApp maps it to, so someone who appears only by LID
// still matches a contact saved under their number. Ingest, the MCP and
// the sell paths all decide "saved" here.
func SavedContact(ctx context.Context, dev *store.Device, jids ...types.JID) (bool, string) {
	var all []types.JID
	seen := map[types.JID]bool{}
	add := func(j types.JID) {
		j = j.ToNonAD()
		if j.User != "" && !seen[j] {
			seen[j] = true
			all = append(all, j)
		}
	}
	for _, j := range jids {
		add(j)
		if dev.LIDs == nil {
			continue
		}
		var alt types.JID
		switch j.Server {
		case types.HiddenUserServer:
			alt, _ = dev.LIDs.GetPNForLID(ctx, j.ToNonAD())
		case types.DefaultUserServer:
			alt, _ = dev.LIDs.GetLIDForPN(ctx, j.ToNonAD())
		}
		add(alt)
	}
	name := ""
	for _, j := range all {
		ci, err := dev.Contacts.GetContact(ctx, j)
		if err != nil || !ci.Found {
			continue
		}
		if ci.FullName != "" || ci.FirstName != "" {
			return true, firstNonEmpty(ci.FullName, ci.FirstName)
		}
		name = firstNonEmpty(name, ci.PushName, ci.BusinessName)
	}
	return false, name
}

// Ingest wires events into the ingester. Call before Connect.
func (c *Client) Ingest(in *ingest.Ingester, st *mstore.Store) {
	c.WA.AddEventHandler(func(evt any) {
		ctx := context.Background()
		var err error
		switch e := evt.(type) {
		case *events.Message:
			err = c.message(ctx, in, e)
		case *events.HistorySync:
			c.history(ctx, in, e)
		case *events.Connected:
			go c.refreshGroups(ctx, st)
		case *events.GroupInfo:
			if in.IsMarketGroup(e.JID.String()) {
				go c.refreshGroups(ctx, st)
			}
		case *events.LoggedOut:
			c.log.Error("WhatsApp logged this device out; run `messwa login` again")
			os.Exit(1)
		}
		if err != nil {
			c.log.Error("ingest", "err", err)
		}
	})
}

func (c *Client) refreshGroups(ctx context.Context, st *mstore.Store) {
	for _, g := range c.cfg.Groups {
		jid, err := types.ParseJID(g)
		if err != nil {
			c.log.Error("bad group JID in config", "jid", g)
			continue
		}
		info, err := c.WA.GetGroupInfo(ctx, jid)
		if err != nil {
			c.log.Warn("group info", "jid", g, "err", err)
			continue
		}
		var admins []string
		for _, p := range info.Participants {
			if p.IsAdmin || p.IsSuperAdmin {
				for _, j := range []types.JID{p.JID, p.PhoneNumber, p.LID} {
					if !j.IsEmpty() {
						admins = append(admins, j.ToNonAD().String())
					}
				}
			}
		}
		if err := st.SetGroup(ctx, g, info.Name, admins); err != nil {
			c.log.Error("save group", "err", err)
		}
	}
}

// history replays a history sync: market-group events through the group
// log (14 days back), DMs into peer activity (2 days back).
func (c *Client) history(ctx context.Context, in *ingest.Ingester, e *events.HistorySync) {
	for _, conv := range e.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil || !c.allowedChat(in, chat) {
			continue
		}
		if chat.Server == types.GroupServer {
			for _, entry := range c.HistoryGroupEntries(ctx, conv) {
				if err := in.Handle(ctx, entry); err != nil {
					c.log.Error("history", "err", err)
				}
			}
			continue
		}
		cutoff := uint64(time.Now().Add(-48 * time.Hour).Unix())
		for _, hm := range conv.GetMessages() {
			wm := hm.GetMessage()
			if wm.GetMessageTimestamp() < cutoff {
				continue
			}
			if evt, err := c.WA.ParseWebMessage(chat, wm); err == nil {
				if err := c.message(ctx, in, evt); err != nil {
					c.log.Error("history", "err", err)
				}
			}
		}
	}
}

// HistoryGroupEntries converts one group conversation from a history sync
// into log entries, oldest first, including reactions stored on messages.
func (c *Client) HistoryGroupEntries(ctx context.Context, conv *waHistorySync.Conversation) []mstore.LogEntry {
	chat, err := types.ParseJID(conv.GetID())
	if err != nil || chat.Server != types.GroupServer {
		return nil
	}
	cutoff := uint64(time.Now().Add(-14 * 24 * time.Hour).Unix())
	var out []mstore.LogEntry
	for _, hm := range conv.GetMessages() {
		wm := hm.GetMessage()
		if wm.GetMessageTimestamp() < cutoff {
			continue
		}
		evt, err := c.WA.ParseWebMessage(chat, wm)
		if err != nil {
			continue
		}
		if entry, ok := c.groupEntry(ctx, evt); ok {
			entry.ChatName = conv.GetName()
			out = append(out, entry)
		}
		for _, r := range wm.GetReactions() {
			k := r.GetKey()
			var reactor types.JID
			if k.GetFromMe() {
				reactor = c.WA.Store.GetJID()
			} else if reactor, err = types.ParseJID(k.GetParticipant()); err != nil {
				continue
			}
			sender, alt := c.identity(ctx, reactor, types.EmptyJID)
			out = append(out, mstore.LogEntry{
				Chat: chat.String(), ID: firstNonEmpty(k.GetID(), evt.Info.ID+":"+sender), Type: "reaction",
				Sender: sender, SenderAlt: alt, TS: time.UnixMilli(r.GetSenderTimestampMS()),
				Text: r.GetText(), Target: evt.Info.ID,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

func (c *Client) allowedChat(in *ingest.Ingester, chat types.JID) bool {
	switch chat.Server {
	case types.GroupServer:
		return in.IsMarketGroup(chat.String())
	case types.DefaultUserServer, types.HiddenUserServer:
		own := c.WA.Store.GetJID().ToNonAD()
		lid := c.WA.Store.GetLID().ToNonAD()
		return chat.ToNonAD() != own && chat.ToNonAD() != lid // skip notes-to-self
	}
	return false // status, newsletters, broadcast lists
}

func (c *Client) message(ctx context.Context, in *ingest.Ingester, e *events.Message) error {
	info := e.Info
	if !c.allowedChat(in, info.Chat) {
		return nil
	}
	m := e.Message
	if info.IsGroup {
		if entry, ok := c.groupEntry(ctx, e); ok {
			return in.Handle(ctx, entry)
		}
		return nil
	}

	if m.GetReactionMessage() != nil || m.GetProtocolMessage() != nil {
		return nil
	}
	peerAlt := info.SenderAlt
	if info.IsFromMe {
		peerAlt = info.RecipientAlt
	}
	peer, alt := c.identity(ctx, info.Chat, peerAlt)
	saved, _ := c.Saved(ctx, peer, alt)
	push := ""
	if !info.IsFromMe {
		push = info.PushName
	}
	return in.DM(ctx, ingest.DirectMessage{
		Peer: peer, PeerAlt: alt, PushName: push, FromMe: info.IsFromMe,
		ID: info.ID, TS: info.Timestamp, Text: textOf(m), Saved: saved,
	})
}

// groupEntry converts a group message event into a log entry.
func (c *Client) groupEntry(ctx context.Context, e *events.Message) (mstore.LogEntry, bool) {
	info, m := e.Info, e.Message
	sender, alt := c.identity(ctx, info.Sender, info.SenderAlt)
	entry := mstore.LogEntry{Chat: info.Chat.String(), ID: info.ID, Sender: sender, SenderAlt: alt, PushName: info.PushName, TS: info.Timestamp}
	if r := m.GetReactionMessage(); r != nil {
		entry.Type, entry.Text, entry.Target = "reaction", r.GetText(), r.GetKey().GetID()
		return entry, true
	}
	if pm := m.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			entry.Type, entry.Target = "delete", pm.GetKey().GetID()
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			entry.Type, entry.Target, entry.Text = "edit", pm.GetKey().GetID(), textOf(pm.GetEditedMessage())
		default:
			return entry, false
		}
		return entry, true
	}
	if sem := m.GetSecretEncryptedMessage(); sem != nil {
		// Newer clients send edits encrypted with the original message's
		// secret, which whatsmeow saved when that message arrived.
		if sem.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
			return entry, false
		}
		dec, err := c.WA.DecryptSecretEncryptedMessage(ctx, e)
		if err != nil {
			c.log.Warn("could not decrypt an edit", "chat", info.Chat.String(), "target", sem.GetTargetMessageKey().GetID(), "err", err)
			return entry, false
		}
		text := textOf(dec)
		if pm := dec.GetProtocolMessage(); pm != nil && pm.GetEditedMessage() != nil {
			text = textOf(pm.GetEditedMessage())
		}
		entry.Type, entry.Target, entry.Text = "edit", sem.GetTargetMessageKey().GetID(), text
		return entry, text != ""
	}
	entry.Type, entry.Text, entry.ReplyTo = "post", textOf(m), replyTo(m)
	return entry, entry.Text != ""
}

// identity returns (phone-number JID if known, LID) or the reverse when
// only the LID is known. Device suffixes are dropped.
func (c *Client) identity(ctx context.Context, jid, alt types.JID) (string, string) {
	jid, alt = jid.ToNonAD(), alt.ToNonAD()
	if jid.Server == types.HiddenUserServer {
		if alt.IsEmpty() {
			if pn, err := c.WA.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
				alt = pn.ToNonAD()
			}
		}
		if !alt.IsEmpty() {
			jid, alt = alt, jid
		}
	} else if alt.IsEmpty() && jid.Server == types.DefaultUserServer {
		if lid, err := c.WA.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lid.IsEmpty() {
			alt = lid.ToNonAD()
		}
	}
	a := ""
	if !alt.IsEmpty() {
		a = alt.String()
	}
	return jid.String(), a
}

func textOf(m *waE2E.Message) string {
	switch {
	case m == nil:
		return ""
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return strings.TrimSpace("[image] " + m.GetImageMessage().GetCaption())
	case m.GetVideoMessage() != nil:
		return strings.TrimSpace("[video] " + m.GetVideoMessage().GetCaption())
	case m.GetDocumentMessage() != nil:
		return strings.TrimSpace("[document] " + m.GetDocumentMessage().GetCaption())
	case m.GetAudioMessage() != nil:
		return "[voice note]"
	case m.GetStickerMessage() != nil:
		return "[sticker]"
	}
	return ""
}

func replyTo(m *waE2E.Message) string {
	if ci := m.GetExtendedTextMessage().GetContextInfo(); ci != nil {
		return ci.GetStanzaID()
	}
	return m.GetImageMessage().GetContextInfo().GetStanzaID()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
