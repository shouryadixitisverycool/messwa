package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// TestUnnamedGroupKeepsAName covers the IIITH - 2 subgroup: the group list
// gave it no subject, so its failures were logged with no name at all.
func TestUnnamedGroupKeepsAName(t *testing.T) {
	jid := types.NewJID("120363416069274282", types.GroupServer)
	joined := []*types.GroupInfo{{JID: jid, AddressingMode: types.AddressingModePN}}
	g, err := pickGroup(joined, jid.String())
	if err != nil || g.Name != jid.String() {
		t.Fatalf("picked %+v, %v; want the JID as its name", g, err)
	}

	conn := newFakeConn(types.NewJID("1", types.GroupServer))
	conn.info = map[types.JID]*types.GroupInfo{jid: {JID: jid, GroupName: types.GroupName{Name: "Mess Buy Sell @ IIITH - 2"}, AddressingMode: types.AddressingModeLID}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fresh := refreshGroup(t.Context(), conn, log, g)
	if fresh.Name != "Mess Buy Sell @ IIITH - 2" || fresh.JID != jid {
		t.Fatalf("refreshed %+v", fresh)
	}
	if len(conn.infoAsked) != 1 || conn.infoAsked[0] != jid {
		t.Fatalf("group queries = %v", conn.infoAsked)
	}

	// A failed query posts anyway, with what the group list said.
	conn.infoErr = errors.New("timed out")
	if again := refreshGroup(t.Context(), conn, log, g); again.Name != g.Name {
		t.Fatalf("after a failed query: %+v", again)
	}
}

// TestEveryCopyQueriesItsGroup checks that the group's details are fetched
// right before each copy is sent.
func TestEveryCopyQueriesItsGroup(t *testing.T) {
	a, b := types.NewJID("1", types.GroupServer), types.NewJID("2", types.GroupServer)
	conn := newFakeConn(a)
	conn.groups = append(conn.groups, &types.GroupInfo{JID: b})
	o := watchGroupOffer(t.Context(), conn, newOfferQueue())
	defer o.close()
	var order []string
	send := func(_ context.Context, _ *groupOffer, g offerTarget, _ int) (postedOffer, error) {
		order = append(order, "send "+g.JID.User)
		if g.JID == b {
			return postedOffer{}, errors.New("server returned error 479")
		}
		return postedOffer{Chat: g.JID, ID: "id-" + g.JID.User}, nil
	}
	o.post(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), []offerTarget{{JID: a, Name: "a"}, {JID: b, Name: b.String()}}, 0, 0, send, nil)
	<-o.donePosting
	if len(conn.infoAsked) != 2 || conn.infoAsked[0] != a || conn.infoAsked[1] != b || len(order) != 2 {
		t.Fatalf("queried %v, sent %v", conn.infoAsked, order)
	}
}
