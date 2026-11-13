package main

import (
	"context"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"messwa/internal/wa"
)

// accountConn never retains a raw client or a bound method from a generation.
type accountConn struct{ *wa.Runtime }

func (a accountConn) GenerateMessageID() (out types.MessageID) {
	_ = a.Do(func(c *wa.Client) error { out = c.WA.GenerateMessageID(); return nil })
	return
}
func (a accountConn) Upload(ctx context.Context, data []byte, kind whatsmeow.MediaType) (out whatsmeow.UploadResponse, err error) {
	err = a.Do(func(c *wa.Client) error { var e error; out, e = c.WA.Upload(ctx, data, kind); return e })
	return
}
func (a accountConn) SendPeerMessage(ctx context.Context, msg *waE2E.Message) (out whatsmeow.SendResponse, err error) {
	err = a.Do(func(c *wa.Client) error { var e error; out, e = c.WA.SendPeerMessage(ctx, msg); return e })
	return
}
func (a accountConn) BuildUnavailableMessageRequest(chat, sender types.JID, id string) (out *waE2E.Message) {
	_ = a.Do(func(c *wa.Client) error { out = c.WA.BuildUnavailableMessageRequest(chat, sender, id); return nil })
	return
}
func (a accountConn) BuildReaction(chat, sender types.JID, id types.MessageID, reaction string) (out *waE2E.Message) {
	_ = a.Do(func(c *wa.Client) error { out = c.WA.BuildReaction(chat, sender, id, reaction); return nil })
	return
}
func (a accountConn) BuildRevoke(chat, sender types.JID, id types.MessageID) (out *waE2E.Message) {
	_ = a.Do(func(c *wa.Client) error { out = c.WA.BuildRevoke(chat, sender, id); return nil })
	return
}
func (a accountConn) BuildEdit(chat types.JID, id types.MessageID, newContent *waE2E.Message) (out *waE2E.Message) {
	_ = a.Do(func(c *wa.Client) error { out = c.WA.BuildEdit(chat, id, newContent); return nil })
	return
}
func (a accountConn) GetJoinedGroups(ctx context.Context) (out []*types.GroupInfo, err error) {
	err = a.Do(func(c *wa.Client) error { var e error; out, e = c.WA.GetJoinedGroups(ctx); return e })
	return
}
func (a accountConn) GetGroupInfo(ctx context.Context, jid types.JID) (out *types.GroupInfo, err error) {
	err = a.Do(func(c *wa.Client) error { var e error; out, e = c.WA.GetGroupInfo(ctx, jid); return e })
	return
}
func (a accountConn) PNForLID(ctx context.Context, lid types.JID) (out types.JID) {
	_ = a.Do(func(c *wa.Client) error {
		if c.WA.Store.LIDs != nil {
			out = (waConn{c.WA}).PNForLID(ctx, lid)
		}
		return nil
	})
	return
}
func (a accountConn) SavedContact(ctx context.Context, jids ...types.JID) (out bool) {
	_ = a.Do(func(c *wa.Client) error { out = (waConn{c.WA}).SavedContact(ctx, jids...); return nil })
	return
}
