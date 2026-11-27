package mcpserver

import (
	"messwa/internal/config"
	"messwa/internal/store"
	"path/filepath"
	"testing"
)

func TestRecurringAlertsAreScopedAndDeduplicated(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	srv := New(config.Default(), st, nil)
	r, err := st.SaveRecurringRule(ctx, store.RecurringRule{UserID: 7, Name: "Lunches", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecurringRuleError(ctx, r.ID, r.Revision, "Portal unavailable"); err != nil {
		t.Fatal(err)
	}
	items, err := srv.Attention(ctx, 7)
	if err != nil || len(items) != 1 || items[0].Rule != r.ID {
		t.Fatalf("rule alert %+v %v", items, err)
	}
	others, err := srv.Attention(ctx, 8)
	if err != nil || len(others) != 0 {
		t.Fatal("another user's rule issue disclosed")
	}
	if err := srv.ResolveAttention(ctx, 7, items[0].ID, items[0].Kind); err != nil {
		t.Fatal(err)
	}
	if err := st.RecurringRuleError(ctx, r.ID, r.Revision, "Portal unavailable"); err != nil {
		t.Fatal(err)
	}
	again, err := srv.Attention(ctx, 7)
	if err != nil || len(again) != 0 {
		t.Fatal("unchanged resolved issue returned")
	}
	if err := st.RecurringRuleError(ctx, r.ID, r.Revision, "API key rejected"); err != nil {
		t.Fatal(err)
	}
	changed, err := srv.Attention(ctx, 7)
	if err != nil || len(changed) != 1 || changed[0].ID == items[0].ID {
		t.Fatal("changed failure was not surfaced")
	}
}
