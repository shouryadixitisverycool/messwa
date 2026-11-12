package wa

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	waBinary "go.mau.fi/whatsmeow/binary"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// LogEnv names the environment variable that sets how much whatsmeow logs:
// warn (the default), info or debug. debug includes a summary of every
// stanza sent and received.
const LogEnv = "MESSWA_WA_LOG"

// LogLevel is the level LogEnv asks for, warn when it's unset or unknown.
func LogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(LogEnv))) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	}
	return slog.LevelWarn
}

// waLogger sends whatsmeow's log lines to slog. Stanzas in its arguments
// are cut down to their tags and attributes first, so message bodies,
// encrypted or not, never reach the log.
type waLogger struct {
	log    *slog.Logger
	module string
	min    slog.Level
}

func newWALogger(log *slog.Logger, module string) waLog.Logger {
	return waLogger{log: log.With("component", "whatsmeow"), module: module, min: LogLevel()}
}

func (l waLogger) Errorf(msg string, args ...any) { l.logf(slog.LevelError, msg, args) }
func (l waLogger) Warnf(msg string, args ...any)  { l.logf(slog.LevelWarn, msg, args) }
func (l waLogger) Infof(msg string, args ...any)  { l.logf(slog.LevelInfo, msg, args) }
func (l waLogger) Debugf(msg string, args ...any) { l.logf(slog.LevelDebug, msg, args) }

func (l waLogger) Sub(module string) waLog.Logger {
	l.module += "/" + module
	return l
}

func (l waLogger) logf(level slog.Level, msg string, args []any) {
	if level < l.min {
		return
	}
	safe := make([]any, len(args))
	for i, a := range args {
		safe[i] = redactNode(a)
	}
	l.log.Log(context.Background(), level, fmt.Sprintf(msg, safe...), "module", l.module)
}

// redactNode replaces a stanza with nodeSummary and leaves anything else.
func redactNode(a any) any {
	switch n := a.(type) {
	case waBinary.Node:
		return nodeSummary{n}
	case *waBinary.Node:
		if n == nil {
			return a
		}
		return nodeSummary{*n}
	case []waBinary.Node:
		out := make([]nodeSummary, len(n))
		for i, c := range n {
			out[i] = nodeSummary{c}
		}
		return out
	}
	return a
}

// nodeSummary prints a stanza as its tags and attributes, a few children
// deep. Byte content shows only its length.
type nodeSummary struct{ n waBinary.Node }

const (
	summaryDepth    = 3
	summaryChildren = 6
)

func (s nodeSummary) String() string {
	var b strings.Builder
	writeNode(&b, s.n, 0)
	return b.String()
}

func writeNode(b *strings.Builder, n waBinary.Node, depth int) {
	b.WriteString("<" + n.Tag)
	for _, k := range sortedKeys(n.Attrs) {
		fmt.Fprintf(b, " %s=%v", k, n.Attrs[k])
	}
	switch c := n.Content.(type) {
	case nil:
		b.WriteString("/>")
		return
	case []byte:
		fmt.Fprintf(b, ">[%d bytes]", len(c))
	case []waBinary.Node:
		b.WriteString(">")
		if depth+1 >= summaryDepth {
			fmt.Fprintf(b, "[%d children]", len(c))
			break
		}
		for i, child := range c {
			if i == summaryChildren {
				fmt.Fprintf(b, "[+%d more]", len(c)-i)
				break
			}
			writeNode(b, child, depth+1)
		}
	default:
		b.WriteString(">[content]")
	}
	b.WriteString("</" + n.Tag + ">")
}

func sortedKeys(m waBinary.Attrs) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
