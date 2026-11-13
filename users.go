package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/portal"
	"messwa/internal/store"
)

const userUsage = `usage: messwa user <command>

  add [-admin] NAME          create an account (asks for a password). The first
                             admin also gets MESS_API_KEY moved onto it.
  list                       every account, its role, and whether it has a mess key
  passwd NAME                set a new password and sign that user out everywhere
  role NAME admin|member     change someone's role
  disable NAME / enable NAME block or unblock sign-in and MCP tokens
  remove -yes NAME           delete an account with its sessions and tokens
  mess-key [-clear] NAME     save (asks for it) or clear that user's mess API key
  token [-label L] NAME      create an MCP token; it's printed once
  tokens [NAME]              list MCP tokens
  revoke ID                  revoke an MCP token

Mess keys are encrypted with MESSWA_SECRET_KEY, which must be set to save or
use them. Generate one with: openssl rand -hex 32
`

func userCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" {
		fmt.Fprint(os.Stderr, userUsage)
		return nil
	}
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return err
	}
	defer st.Close()
	acc, err := accounts.FromEnv(st)
	if err != nil {
		return err
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("user "+cmd, flag.ContinueOnError)
	admin := fs.Bool("admin", false, "make the account an admin")
	label := fs.String("label", "", "what the token is for, e.g. laptop")
	clear := fs.Bool("clear", false, "remove the saved key")
	yes := fs.Bool("yes", false, "confirm removal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	one := func() (*accounts.User, error) {
		if len(pos) != 1 {
			return nil, fmt.Errorf("user %s takes one NAME", cmd)
		}
		return acc.ByName(ctx, pos[0])
	}

	switch cmd {
	case "add":
		if len(pos) != 1 {
			return errors.New("usage: messwa user add [-admin] NAME")
		}
		pw, err := newPassword()
		if err != nil {
			return err
		}
		role := accounts.Member
		if *admin {
			role = accounts.Admin
		}
		u, err := acc.Add(ctx, pos[0], pw, role)
		if err != nil {
			return err
		}
		fmt.Printf("Created %s (%s).\n", u.Name, u.Role)
		if *admin {
			moved, warning, err := acc.MigrateEnvKey(ctx)
			if err != nil {
				return err
			}
			if moved != "" {
				fmt.Printf("Moved %s onto %s, encrypted. Remove it from .env.\n", accounts.LegacyKeyEnv, moved)
			} else if warning != "" {
				fmt.Println(warning)
			}
		}
		return nil

	case "list":
		us, err := acc.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tROLE\tMESS KEY\tSTATUS")
		for _, u := range us {
			key, status := "no", "active"
			if u.HasMessKey {
				key = "saved " + u.MessKeyAt.Format("2006-01-02")
			}
			if u.Disabled {
				status = "disabled"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Name, u.Role, key, status)
		}
		return tw.Flush()

	case "passwd":
		u, err := one()
		if err != nil {
			return err
		}
		pw, err := newPassword()
		if err != nil {
			return err
		}
		if err := acc.SetPassword(ctx, u.ID, pw); err != nil {
			return err
		}
		fmt.Printf("Password changed; %s is signed out everywhere.\n", u.Name)
		return nil

	case "role":
		if len(pos) != 2 {
			return errors.New("usage: messwa user role NAME admin|member")
		}
		u, err := acc.ByName(ctx, pos[0])
		if err != nil {
			return err
		}
		role, err := accounts.ParseRole(pos[1])
		if err != nil {
			return err
		}
		return acc.SetRole(ctx, u.ID, role)

	case "disable", "enable":
		u, err := one()
		if err != nil {
			return err
		}
		return acc.SetDisabled(ctx, u.ID, cmd == "disable")

	case "remove":
		u, err := one()
		if err != nil {
			return err
		}
		if !*yes {
			return fmt.Errorf("this deletes %s with their mess key and MCP tokens; rerun with -yes", u.Name)
		}
		return acc.Remove(ctx, u.ID)

	case "mess-key":
		u, err := one()
		if err != nil {
			return err
		}
		if *clear {
			return acc.ClearMessKey(ctx, u.ID)
		}
		if !acc.CanStoreKeys() {
			return accounts.ErrNoSecret
		}
		key, err := readSecret("Mess API key for " + u.Name + ": ")
		if err != nil {
			return err
		}
		if err := acc.SetMessKey(ctx, u.ID, key); err != nil {
			return err
		}
		fmt.Printf("Saved, encrypted, for %s.\n", u.Name)
		return nil

	case "token":
		u, err := one()
		if err != nil {
			return err
		}
		tok, _, err := acc.CreateToken(ctx, u.ID, *label)
		if err != nil {
			return err
		}
		fmt.Printf("MCP token for %s (shown once, store it now):\n\n  %s\n\nclaude mcp add --transport http messwa http://%s/mcp --header \"Authorization: Bearer %s\"\n", u.Name, tok, cfg.MCP.Listen, tok)
		return nil

	case "tokens":
		var id int64
		names := map[int64]string{}
		us, err := acc.List(ctx)
		if err != nil {
			return err
		}
		for _, u := range us {
			names[u.ID] = u.Name
		}
		if len(pos) == 1 {
			u, err := acc.ByName(ctx, pos[0])
			if err != nil {
				return err
			}
			id = u.ID
		}
		ts, err := acc.Tokens(ctx, id)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tUSER\tLABEL\tTOKEN\tCREATED\tLAST USED\tSTATUS")
		for _, t := range ts {
			used, status := "never", "active"
			if !t.LastUsed.IsZero() {
				used = t.LastUsed.Format("2006-01-02 15:04")
			}
			if t.Revoked {
				status = "revoked"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s…\t%s\t%s\t%s\n", t.ID, names[t.UserID], t.Label, t.Prefix, t.CreatedAt.Format("2006-01-02"), used, status)
		}
		return tw.Flush()

	case "revoke":
		if len(pos) != 1 {
			return errors.New("usage: messwa user revoke ID")
		}
		id, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil {
			return errors.New("ID is the number from messwa user tokens")
		}
		return acc.RevokeToken(ctx, 0, id)
	}
	fmt.Fprint(os.Stderr, userUsage)
	return fmt.Errorf("unknown user command %q", cmd)
}

// newPassword asks for a password twice.
func newPassword() (string, error) {
	pw, err := readSecret("Password: ")
	if err != nil {
		return "", err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := readSecret("Again: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("the passwords don't match")
		}
	}
	return pw, nil
}

var stdin = bufio.NewReader(os.Stdin)

// readSecret reads a line without echo on a terminal, or plainly from a
// pipe, so scripts can pass it on stdin.
func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// userPortal returns a portal client with a user's own mess API key. An
// empty name means the owner: the first admin, or MESS_API_KEY before any
// admin exists.
func userPortal(ctx context.Context, cfg *config.Config, name string) (*portal.Client, error) {
	if cfg.AccountRoot != "" {
		c := *cfg
		c.StateDir = cfg.AccountRoot
		cfg = &c
	}
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return nil, err
	}
	defer st.Close()
	acc, err := accounts.FromEnv(st)
	if err != nil {
		return nil, err
	}
	var u *accounts.User
	if name == "" {
		u, err = acc.Owner(ctx)
	} else {
		u, err = acc.ByName(ctx, name)
	}
	if err != nil {
		return nil, err
	}
	return acc.Portal(ctx, u.ID)
}
