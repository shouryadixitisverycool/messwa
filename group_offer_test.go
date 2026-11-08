package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestOfferQueueKeepsReplyOrder(t *testing.T) {
	q := offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
	a := types.NewJID("100", types.DefaultUserServer)
	b := types.NewJID("200", types.DefaultUserServer)
	c := types.NewJID("300", types.DefaultUserServer)
	q.add(a)
	if peer, err := q.next(t.Context()); err != nil || peer != a {
		t.Fatalf("first buyer = %v, %v", peer, err)
	}
	q.add(b)
	q.add(a)
	q.add(c)
	q.add(b)
	for _, want := range []types.JID{b, c} {
		if peer, err := q.next(t.Context()); err != nil || peer != want {
			t.Fatalf("next buyer = %v, %v; want %v", peer, err, want)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if _, err := q.next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("duplicate reply stayed in queue: %v", err)
	}
}

func TestBuyerPassed(t *testing.T) {
	if !buyerPassed(&waE2E.Message{Conversation: proto.String(" No thanks! ")}) {
		t.Fatal("decline was ignored")
	}
	if buyerPassed(&waE2E.Message{Conversation: proto.String("Don't cancel, I paid")}) {
		t.Fatal("ordinary conversation was treated as a decline")
	}
}

func TestSoldOfferDrainsOnlyWaitingBuyers(t *testing.T) {
	q := offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
	first := types.NewJID("100", types.DefaultUserServer)
	waiting := types.NewJID("200", types.DefaultUserServer)
	q.add(first)
	q.add(waiting)
	q.add(waiting)
	if peer, err := q.next(t.Context()); err != nil || peer != first {
		t.Fatalf("first buyer = %v, %v", peer, err)
	}
	if buyers := q.closeAndDrain(); len(buyers) != 1 || buyers[0] != waiting {
		t.Fatalf("sold notifications would go to %v", buyers)
	}
	q.add(types.NewJID("300", types.DefaultUserServer))
	if buyers := q.closeAndDrain(); len(buyers) != 0 {
		t.Fatalf("closed offer accepted new buyers: %v", buyers)
	}
}

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
