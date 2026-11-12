package wa

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
)

func TestWALoggerHidesStanzaContent(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Setenv(LogEnv, "debug")
	l := newWALogger(log, "Client").Sub("Send")
	node := waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"to": "120363416069274282@g.us", "addressing_mode": "lid"}, Content: []waBinary.Node{
		{Tag: "enc", Attrs: waBinary.Attrs{"type": "skmsg"}, Content: []byte("secret ciphertext")},
		{Tag: "plaintext", Content: []byte("sell kadamba veg lunch")},
	}}
	l.Debugf("%s", &node)
	l.Warnf("Node handling took %s for %s", "2s", node)
	out := buf.String()
	if strings.Contains(out, "secret") || strings.Contains(out, "kadamba") {
		t.Fatalf("log has stanza content: %s", out)
	}
	for _, want := range []string{"addressing_mode=lid", "to=120363416069274282@g.us", "<enc type=skmsg>[17 bytes]</enc>", "module=Client/Send", "component=whatsmeow"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
}

func TestWALoggerLevel(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Setenv(LogEnv, "")
	l := newWALogger(log, "Client")
	l.Debugf("debug line")
	l.Infof("info line")
	l.Warnf("warn line")
	l.Errorf("error line")
	out := buf.String()
	if strings.Contains(out, "debug line") || strings.Contains(out, "info line") || !strings.Contains(out, "warn line") || !strings.Contains(out, "error line") {
		t.Fatalf("default level logged: %s", out)
	}
	for v, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "error": slog.LevelError, "loud": slog.LevelWarn} {
		t.Setenv(LogEnv, v)
		if got := LogLevel(); got != want {
			t.Errorf("%s=%q gives %v, want %v", LogEnv, v, got, want)
		}
	}
}
