package main

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestPrivateOfferReply(t *testing.T) {
	group := types.NewJID("123", types.GroupServer)
	postedAt := time.Unix(1793900000, 0)
	newReply := func() *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: types.NewJID("456", types.HiddenUserServer)},
				Timestamp:     postedAt.Add(time.Second),
			},
			Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String("Interested"),
				ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("offer"), RemoteJID: proto.String(group.String())},
			}},
		}
	}
	if !privateOfferReply(newReply(), group, "offer", postedAt) {
		t.Fatal("private reply quoting the offer did not match")
	}
	withoutRemote := newReply()
	withoutRemote.Message.ExtendedTextMessage.ContextInfo.RemoteJID = nil
	if !privateOfferReply(withoutRemote, group, "offer", postedAt) {
		t.Fatal("exact quote ID should match when the client omits the group JID")
	}
	for name, alter := range map[string]func(*events.Message){
		"plain DM":    func(e *events.Message) { e.Message = &waE2E.Message{Conversation: proto.String("Interested")} },
		"other quote": func(e *events.Message) { e.Message.ExtendedTextMessage.ContextInfo.StanzaID = proto.String("other") },
		"other group": func(e *events.Message) {
			e.Message.ExtendedTextMessage.ContextInfo.RemoteJID = proto.String("789@g.us")
		},
		"group reply": func(e *events.Message) { e.Info.IsGroup = true; e.Info.Chat = group },
		"own message": func(e *events.Message) { e.Info.IsFromMe = true },
		"stale DM":    func(e *events.Message) { e.Info.Timestamp = postedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newReply()
			alter(e)
			if privateOfferReply(e, group, "offer", postedAt) {
				t.Fatal("unrelated message matched the offer")
			}
		})
	}
}
