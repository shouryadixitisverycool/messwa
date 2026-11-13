package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"strings"
	"time"

	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

// chat prints the merged stream of all market groups. Each post shows the
// listing it landed in; "dup" marks posts merged into an earlier listing and
// "-" marks posts that weren't treated as listings.
func chat(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	hours := fs.Int("hours", 48, "look back this many hours")
	fs.Parse(args)
	st, err := openCommandStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	lines, err := st.Log(ctx, time.Now().Add(-time.Duration(*hours)*time.Hour))
	if err != nil {
		return err
	}
	names, _ := st.GroupNames(ctx)
	people := contactNames(cfg)
	short := map[string]string{}
	for i, g := range cfg.Groups {
		short[g] = fmt.Sprintf("G%d", i+1)
		fmt.Printf("G%d = %s\n", i+1, firstNonEmpty(names[g], g))
	}
	fmt.Println()
	listingOf := map[string]int64{} // chat/id -> listing, for reactions
	seen := map[int64]bool{}
	var posts, listed, dups int
	loc := cfg.Location()
	for _, l := range lines {
		who := firstNonEmpty(people[l.Sender], people[l.SenderAlt], l.PushName, phone(l.Sender), l.Sender)
		ts := l.TS.In(loc).Format("01-02 15:04")
		switch l.Type {
		case "post":
			posts++
			mark := "-"
			if l.ListingID > 0 {
				listed++
				listingOf[l.Chat+"/"+l.ID] = l.ListingID
				mark = fmt.Sprintf("#%d", l.ListingID)
				if seen[l.ListingID] {
					mark += " dup"
					dups++
				}
				seen[l.ListingID] = true
			}
			reply := ""
			if l.ReplyTo != "" {
				reply = "↩ "
			}
			fmt.Printf("%s %s %-18.18s %-9s %s%s\n", ts, short[l.Chat], who, mark, reply, oneLine(l.Text))
		case "reaction":
			emoji := firstNonEmpty(l.Text, "(removed)")
			fmt.Printf("%s %s %-18.18s %-9s %s on %s\n", ts, short[l.Chat], who, "react", emoji, target(listingOf, l))
		case "edit":
			fmt.Printf("%s %s %-18.18s %-9s %s: %s\n", ts, short[l.Chat], who, "edit", target(listingOf, l), oneLine(l.Text))
		case "delete":
			fmt.Printf("%s %s %-18.18s %-9s %s\n", ts, short[l.Chat], who, "delete", target(listingOf, l))
		}
	}
	fmt.Printf("\n%d posts: %d became %d listings (%d merged as duplicates), %d not listings\n", posts, listed, len(seen), dups, posts-listed)
	return nil
}

func listings(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("listings", flag.ExitOnError)
	hours := fs.Int("hours", 48, "look back this many hours")
	fs.Parse(args)
	st, err := openCommandStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	out, err := mcpserver.New(cfg, st, nil).List(ctx, mcpserver.ListIn{Kind: "any", Status: "all", Hours: *hours, Limit: 200})
	if err != nil {
		return err
	}
	for _, l := range out.Listings {
		price := "-"
		if l.Price != nil {
			price = fmt.Sprintf("₹%d", *l.Price)
		}
		mess := firstNonEmpty(l.Mess, "-")
		if len(l.Wants) > 0 {
			mess += "→" + strings.Join(l.Wants, "/")
		}
		fmt.Printf("#%-4d %-9s %-8s %-9s %-5s %-16s %-6s %-5s %-7s %dg  %-14.14s %s\n",
			l.ID, l.Status, firstNonEmpty(l.Kind, "?"), l.Meal, l.MealDate[5:], mess, firstNonEmpty(l.Diet, "-"), price,
			l.MealSource, len(l.Groups), firstNonEmpty(l.Seller.Name, l.Seller.Phone), oneLine(l.Text))
	}
	fmt.Printf("\n%d listings\n", len(out.Listings))
	return nil
}

// contactNames reads names from whatsmeow's contact store: your saved name
// for the person if any, else the name they set themselves.
func contactNames(cfg *config.Config) map[string]string {
	m := map[string]string{}
	db, err := sql.Open("sqlite", "file:"+cfg.SessionDB()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return m
	}
	defer db.Close()
	rows, err := db.Query(`SELECT their_jid, coalesce(nullif(full_name,''), nullif(first_name,''), nullif(push_name,''), '') FROM whatsmeow_contacts`)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var j, n string
		if rows.Scan(&j, &n) == nil && n != "" {
			m[j] = n
		}
	}
	return m
}

func rebuild(ctx context.Context, cfg *config.Config) error {
	st, err := openCommandStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := ingest.New(cfg, st).Rebuild(ctx)
	if err == nil {
		fmt.Printf("replayed %d events\n", n)
	}
	return err
}

func target(listingOf map[string]int64, l store.LogLine) string {
	if id := listingOf[l.Chat+"/"+l.Target]; id > 0 {
		return fmt.Sprintf("#%d", id)
	}
	return "a non-listing"
}

func phone(jid string) string {
	if u, ok := strings.CutSuffix(jid, "@s.whatsapp.net"); ok {
		return "+" + u
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 90 {
		return string(r[:90]) + "…"
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// dbContacts names people from the session database's contact table, for
// commands that don't connect to WhatsApp. It never reports anyone as saved.
type dbContacts map[string]string

func (d dbContacts) Saved(_ context.Context, jids ...string) (bool, string) {
	for _, j := range jids {
		if n := d[j]; n != "" {
			return false, n
		}
	}
	return false, ""
}

func contactsFromDB(cfg *config.Config) mcpserver.Contacts { return dbContacts(contactNames(cfg)) }
