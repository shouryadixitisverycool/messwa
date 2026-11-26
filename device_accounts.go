package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/dashboard"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/notify"
	"messwa/internal/store"
	"messwa/internal/wa"
	"rsc.io/qr"
)

type deviceAccounts struct {
	readOnly bool
	clock    func() time.Time
	mu       sync.Mutex
	ctx      context.Context
	cfg      *config.Config
	log      *slog.Logger
	st       *store.Store
	acc      *accounts.Accounts
	hub      *dashboard.Hub
	users    map[int64]*deviceAccount
	// extraChannels builds additional notify.Channel instances (such as Web Push)
	// registered on every per-account Dispatcher.
	extraChannels []func(userID int64, cfg *config.Config, st *store.Store) notify.Channel
}
type deviceAccount struct {
	id int64
	// started, notifierStarted, notifierDone and unlinking are guarded by deviceAccounts.mu.
	started         bool
	notifierStarted bool
	notifierDone    chan struct{}
	unlinking       bool

	// notifier is lock-free so callbacks fired while a.mu is held (such as
	// sell.Cancel inside Unlink) can wake it without re-entering a.mu.
	notifier atomic.Pointer[notify.Dispatcher]

	mu      sync.Mutex
	ctx     context.Context
	stop    context.CancelFunc
	client  *wa.Runtime
	srv     *mcpserver.Server
	st      *store.Store
	sell    *sellManager
	pairing bool
	// pairStart, stopPair and done belong to the running pairing session;
	// done closes when it ends. code is the phone-number pairing code for
	// it, when one was asked for.
	pairStart time.Time
	stopPair  context.CancelFunc
	done      chan struct{}
	code      string
	qr        string
	err       string
}

func accountConfig(ctx context.Context, cfg *config.Config, st *store.Store, id int64) (*config.Config, error) {
	dir, err := st.AccountDir(ctx, cfg.StateDir, id)
	if err != nil {
		return nil, err
	}
	// The account in the main state directory shares cfg's database, so
	// its settings are already in cfg.Live(). Start it from config.toml:
	// starting from Live would make its saved settings the defaults, and
	// the settings page would then drop them on the next save.
	base := cfg.Live()
	if dir == cfg.StateDir {
		base = cfg.File()
	}
	// A fresh live pointer prevents one user's settings affecting another.
	c, err := base.WithSettings(nil)
	if err != nil {
		return nil, err
	}
	c.IsolateSettings()
	c.StateDir = dir
	if dir != cfg.StateDir {
		c.MCP.SellTasks = cfg.Live().MCP.NewUserSellTasks
		c.Sell = config.Default().Sell
		c.Sales = config.Sales{}
		c.Alerts = config.Default().Alerts
	}
	if err = config.EnsureDir(dir); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *deviceAccounts) server(ctx context.Context, id int64) (*mcpserver.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.acc.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil || u.Disabled {
		return nil, fmt.Errorf("account unavailable")
	}
	if a := m.users[id]; a != nil {
		m.settle(a, u)
		return a.srv, nil
	}
	cfg, err := accountConfig(ctx, m.cfg, m.st, id)
	if err != nil {
		return nil, err
	}
	var st *store.Store
	if cfg.StateDir == m.cfg.StateDir {
		st = m.st
	} else {
		st, err = store.OpenPrivate(cfg.DataDB(), m.st)
		if err != nil {
			return nil, err
		}
	}
	ready := false
	defer func() {
		if !ready && st != m.st {
			st.Close()
		}
	}()
	saved, _, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	accountLog := m.log.With("user_id", id)
	for k, skipErr := range cfg.ApplySaved(config.Settings(saved)) {
		accountLog.Warn("ignoring saved account setting; it no longer fits config.toml", "key", k, "err", skipErr)
	}
	if err = st.AssignLegacyTasks(ctx, id); err != nil {
		return nil, err
	}
	c, err := wa.Open(m.ctx, cfg, accountLog)
	if err != nil {
		return nil, err
	}
	accountCtx, stop := context.WithCancel(m.ctx)
	a := &deviceAccount{ctx: accountCtx, st: st, stop: stop}
	in := ingest.New(cfg, st)
	in.OnChange(m.hub.Notify)
	client := wa.NewRuntime(accountCtx, c, in, st)
	a.client = client
	wakeNotifier := func() {
		if n := a.notifier.Load(); n != nil {
			n.Wake()
		}
	}
	// The usage page's live view shows who is offline, and the notifier wakes
	// on connection state changes (re-enabling offline items on reconnect).
	client.AddEventHandler(func(evt any) {
		switch evt.(type) {
		case *events.Connected:
			m.hub.Notify()
			if n := a.notifier.Load(); n != nil {
				n.ChannelOnline(notify.ChannelWhatsApp)
			}
		case *events.Disconnected, *events.LoggedOut:
			m.hub.Notify()
			wakeNotifier()
		}
	})
	send := func(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
		if err := accountCtx.Err(); err != nil {
			return whatsmeow.SendResponse{}, err
		}
		user, err := m.acc.User(ctx, id)
		if err != nil {
			return whatsmeow.SendResponse{}, err
		}
		if user == nil || !user.IsActive() {
			return whatsmeow.SendResponse{}, fmt.Errorf("account unavailable")
		}
		var resp whatsmeow.SendResponse
		err = client.Do(func(c *wa.Client) error {
			var err error
			resp, err = c.WA.SendMessage(ctx, to, msg, extra...)
			return err
		})
		return resp, err
	}
	sell := newSellManager(accountCtx, cfg, accountLog, st, accountConn{client}, send, client.WaitForConnection,
		func(ctx context.Context, userID int64) (mealPortal, error) {
			if userID != id {
				return nil, fmt.Errorf("account mismatch")
			}
			return proxyPortal{stateDir: cfg.StateDir, real: func(ctx context.Context) (mealPortal, error) { return m.acc.Portal(ctx, id) }, friends: st.FriendMeal}, nil
		}, newOwnGroupLog(accountCtx, cfg, st, client))
	sell.notify = func() {
		m.hub.Notify()
		wakeNotifier()
	}
	a.sell = sell
	a.srv = mcpserver.New(cfg, st, client)
	a.srv.SetAccounts(m.acc)
	if m.clock != nil {
		a.srv.SetClock(m.clock)
	}
	a.srv.SetSellTasks(sell)
	a.srv.BindAccount(id, a)
	if m.readOnly {
		a.srv.Device = nil
		a.srv.SetSellTasks(nil)
	} else {
		n := notify.NewDispatcher(notify.DispatcherConfig{
			UserID: id,
			Store:  st,
			Config: func() *config.Config { return a.srv.Config().Live() },
			Source: func(ctx context.Context) ([]notify.Item, error) {
				user, err := m.acc.User(ctx, id)
				if err != nil || user == nil || !user.IsActive() {
					return nil, err
				}
				return attentionToNotifyItems(a.srv.Attention(ctx, id))
			},
			BaseURL: func() string { return m.cfg.Dash.BaseURL() },
			Log:     accountLog,
			Now:     m.clock,
		})
		n.Register(notify.NewWhatsAppChannel(waAlertSender{
			accountCtx: accountCtx,
			acc:        m.acc,
			userID:     id,
			client:     client,
		}))
		for _, build := range m.extraChannels {
			if ch := build(id, cfg, st); ch != nil {
				n.Register(ch)
			}
		}
		a.notifier.Store(n)
	}
	ready = true
	a.id = id
	m.users[id] = a
	go reloadSettings(accountCtx, m.log, a.srv)
	m.settle(a, u)
	return a.srv, nil
}

type waAlertSender struct {
	accountCtx context.Context
	acc        *accounts.Accounts
	userID     int64
	client     *wa.Runtime
}

// SendSelfText checks account status, resolves the account's own non-AD JID,
// and sends within a single wa.Runtime lease so unlinking and relinking a
// different phone number cannot redirect an alert to the previous number.
func (s waAlertSender) SendSelfText(ctx context.Context, text string) error {
	if err := s.accountCtx.Err(); err != nil {
		return err
	}
	user, err := s.acc.User(ctx, s.userID)
	if err != nil {
		return err
	}
	if user == nil || !user.IsActive() {
		return fmt.Errorf("%w: account unavailable", notify.ErrChannelUnavailable)
	}
	return s.client.Do(func(c *wa.Client) error {
		if !c.LoggedIn() || !c.WA.IsConnected() {
			return fmt.Errorf("%w: WhatsApp disconnected", notify.ErrChannelUnavailable)
		}
		jidStr, altStr, _ := c.Self(ctx)
		var selfJID types.JID
		for _, raw := range []string{jidStr, altStr} {
			if raw == "" {
				continue
			}
			if j, err := types.ParseJID(raw); err == nil && !j.IsEmpty() && j.User != "" {
				selfJID = j.ToNonAD()
				break
			}
		}
		if selfJID.IsEmpty() {
			return fmt.Errorf("%w: WhatsApp not linked", notify.ErrChannelUnavailable)
		}
		_, err := c.WA.SendMessage(ctx, selfJID, &waE2E.Message{Conversation: proto.String(text)})
		return err
	})
}

func attentionToNotifyItems(items []mcpserver.AttentionItem, err error) ([]notify.Item, error) {
	if err != nil {
		return nil, err
	}
	out := make([]notify.Item, len(items))
	for i, it := range items {
		out[i] = notify.Item{
			ID:         it.ID,
			Kind:       it.Kind,
			Title:      it.Title,
			Detail:     it.Detail,
			Date:       it.Date,
			Meal:       it.Meal,
			Task:       it.Task,
			Account:    it.Account,
			Resolvable: it.Resolvable,
			Rule:       it.Rule,
			Buyer:      it.Buyer,
		}
	}
	return out, nil
}

// settle brings a user's WhatsApp client in line with their sign-up status.
// An approved account's linked client connects and resumes its sell tasks,
// once. One an admin declined gets its device unlinked. Accounts still
// waiting stay as they are: they can link, but run doesn't connect them at
// start. Every dashboard request looks the user up, so an approval from the
// CLI takes effect on that user's next page load. m.mu must be held.
func (m *deviceAccounts) settle(a *deviceAccount, u *accounts.User) {
	if m.readOnly {
		return
	}
	if n := a.notifier.Load(); u.IsActive() && n != nil && !a.notifierStarted {
		a.notifierStarted = true
		done := make(chan struct{})
		a.notifierDone = done
		go func() {
			defer close(done)
			n.Run(a.ctx)
		}()
	}
	switch {
	case u.IsActive() && !a.started && a.client.LoggedIn():
		a.started = true
		if err := a.sell.Resume(a.ctx, a.srv.Config().MCP.SellTasks); err != nil {
			m.log.Error("resume user's tasks", "user", a.id, "err", err)
		}
		if !a.client.IsConnected() {
			a.client.Reconnect()
		}
	case u.Status == accounts.Declined && a.client.LoggedIn() && !a.unlinking:
		a.unlinking = true
		go func() {
			if err := a.Unlink(a.ctx); err != nil {
				m.log.Error("unlink declined user's WhatsApp", "user", a.id, "err", err)
			}
			m.mu.Lock()
			a.unlinking = false
			m.mu.Unlock()
		}()
	}
}

// decided is the accounts.OnDecision hook: it settles the user's client
// right away instead of on their next page load.
func (m *deviceAccounts) decided(ctx context.Context, id int64) {
	if _, err := m.server(ctx, id); err != nil {
		m.log.Error("settle user's WhatsApp after a decision", "user", id, "err", err)
	}
}

func (a *deviceAccount) Linked() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.client.LoggedIn() }
func (a *deviceAccount) Status() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{"linked": a.client.LoggedIn(), "connected": a.client.IsConnected(), "pairing": a.pairing, "qr": a.qr, "code": a.code, "error": a.err}
}
func (a *deviceAccount) Link(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client.LoggedIn() {
		return fmt.Errorf("already linked")
	}
	if a.pairing {
		return nil
	}
	// When WhatsApp logs the device out (removed from the phone), whatsmeow
	// deletes its session but keeps the client, which then fails every use
	// with "invalid use of deleted device". Start from a fresh device.
	var deleted bool
	_ = a.client.Do(func(c *wa.Client) error { deleted = c.WA.Store.Deleted; return nil })
	if deleted {
		if err := a.client.Unlink(a.ctx); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	var ch <-chan whatsmeow.QRChannelItem
	err := a.client.Do(func(c *wa.Client) error { var err error; ch, err = c.WA.GetQRChannel(ctx); return err })
	if err != nil {
		cancel()
		return err
	}
	a.pairing = true
	a.pairStart = time.Now()
	a.stopPair = cancel
	a.done = make(chan struct{})
	a.err = ""
	a.code = ""
	done := a.done
	go func() {
		defer close(done)
		defer cancel()
		if err := a.client.Do(func(c *wa.Client) error { return c.WA.Connect() }); err != nil {
			a.mu.Lock()
			a.err = err.Error()
			a.pairing = false
			a.mu.Unlock()
			return
		}
		for item := range ch {
			a.mu.Lock()
			if item.Event == "code" {
				code, err := qr.Encode(item.Code, qr.M)
				if err == nil {
					a.qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
				}
			}
			if item.Error != nil {
				a.err = item.Error.Error()
			}
			a.mu.Unlock()
		}
		a.mu.Lock()
		a.pairing = false
		a.qr = ""
		a.code = ""
		a.mu.Unlock()
	}()
	return nil
}

// LinkCode links by phone number instead of QR: it returns a code to type
// into WhatsApp on the phone with that number. A code only lasts as long as
// its pairing session, about 160 seconds from the start, so a session older
// than a minute is restarted first.
func (a *deviceAccount) LinkCode(ctx context.Context, phone string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) == 10 {
		digits = "91" + digits // a bare Indian mobile number
	}
	if len(digits) < 11 || len(digits) > 15 || digits[0] == '0' {
		return "", fmt.Errorf("enter your WhatsApp number with its country code, like +91 98765 43210")
	}
	a.mu.Lock()
	old, stop := a.done, a.stopPair
	stale := a.pairing && time.Since(a.pairStart) > time.Minute
	a.mu.Unlock()
	if stale {
		stop()
		select {
		case <-old:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := a.Link(ctx); err != nil {
		return "", err
	}
	// The session is ready for a code once it has shown its first QR.
	for deadline := time.Now().Add(20 * time.Second); ; {
		a.mu.Lock()
		ready, pairing := a.qr != "", a.pairing
		a.mu.Unlock()
		if ready {
			break
		}
		if !pairing || time.Now().After(deadline) {
			return "", fmt.Errorf("couldn't reach WhatsApp to get a code; try again")
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	var code string
	err := a.client.Do(func(c *wa.Client) error {
		var err error
		code, err = c.WA.PairPhone(ctx, digits, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		return err
	})
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.code = code
	a.mu.Unlock()
	return code, nil
}
func (a *deviceAccount) Unlink(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pairing {
		return fmt.Errorf("wait for pairing to finish before unlinking")
	}
	tasks, err := a.st.SellTasks(ctx, time.Time{})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.State == store.TaskActive {
			if err := a.sell.Cancel(ctx, t.ID, false); err != nil {
				return err
			}
		}
	}
	if err := a.client.Unlink(ctx); err != nil {
		return err
	}
	a.qr = ""
	a.err = ""
	return nil
}
func (m *deviceAccounts) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.users {
		if a.stop != nil {
			a.stop()
		}
		if a.notifierDone != nil {
			<-a.notifierDone
		}
		a.client.Close()
		if a.st != m.st {
			a.st.Close()
		}
	}
}

func (m *deviceAccounts) prune(ctx context.Context, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.users {
		if a.st != m.st {
			if err := a.st.Prune(ctx, now.Add(-a.srv.Config().Live().DMs.Retention.Duration), now.Add(-14*24*time.Hour)); err != nil {
				m.log.Error("prune user's data", "user", id, "err", err)
			}
		}
	}
}

// JoinedGroups lists the groups this WhatsApp is in, JID to name, for the
// welcome page's group step. An account that isn't approved yet has no
// running client, so this connects it when needed and asks to try again.
func (a *deviceAccount) JoinedGroups(ctx context.Context) (map[string]string, error) {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if !c.LoggedIn() {
		return nil, fmt.Errorf("link WhatsApp first")
	}
	if !c.IsConnected() {
		c.Reconnect()
		return nil, dashboard.ErrConnecting
	}
	gs, err := accountConn{c}.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(gs))
	for _, g := range gs {
		out[g.JID.String()] = g.Name
	}
	return out, nil
}
