package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestHomeVisitSessions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "visits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Fresh user has no previous visit.
	if since, err := st.HomeVisitSince(ctx, 1); err != nil || since != 0 {
		t.Fatalf("fresh HomeVisitSince: %d %v", since, err)
	}

	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	// First visit returns 0.
	if since, err := st.PingHomeVisit(ctx, 1, t0); err != nil || since != 0 {
		t.Fatalf("first ping: %d %v", since, err)
	}
	// Reload 10 minutes later stays in the same session.
	t1 := t0.Add(10 * time.Minute)
	if since, err := st.PingHomeVisit(ctx, 1, t1); err != nil || since != 0 {
		t.Fatalf("same-session reload: %d %v", since, err)
	}
	// Reload exactly 30 minutes after the last ping is still in the same session.
	t2 := t1.Add(30 * time.Minute)
	if since, err := st.PingHomeVisit(ctx, 1, t2); err != nil || since != 0 {
		t.Fatalf("boundary 30m reload: %d %v", since, err)
	}

	// Next ping more than 30 minutes later starts a new session with prev_seen = t2.
	t3 := t2.Add(30*time.Minute + time.Second)
	if since, err := st.PingHomeVisit(ctx, 1, t3); err != nil || since != t2.Unix() {
		t.Fatalf("second session start: got %d, want %d (err %v)", since, t2.Unix(), err)
	}
	// Reload within the second session keeps prev_seen = t2.
	t4 := t3.Add(5 * time.Minute)
	if since, err := st.PingHomeVisit(ctx, 1, t4); err != nil || since != t2.Unix() {
		t.Fatalf("second session reload: got %d, want %d (err %v)", since, t2.Unix(), err)
	}

	// Reading without pinging returns prev_seen and does not extend last_seen.
	if since, err := st.HomeVisitSince(ctx, 1); err != nil || since != t2.Unix() {
		t.Fatalf("HomeVisitSince: got %d, want %d (err %v)", since, t2.Unix(), err)
	}
	t5 := t4.Add(31 * time.Minute)
	if since, err := st.PingHomeVisit(ctx, 1, t5); err != nil || since != t4.Unix() {
		t.Fatalf("third session start: got %d, want %d (err %v)", since, t4.Unix(), err)
	}

	// Another user is tracked independently.
	if since, err := st.HomeVisitSince(ctx, 2); err != nil || since != 0 {
		t.Fatalf("user 2 before ping: %d %v", since, err)
	}
	if since, err := st.PingHomeVisit(ctx, 2, t5); err != nil || since != 0 {
		t.Fatalf("user 2 first ping: %d %v", since, err)
	}
}

func TestResolvedAttentionCountAndMigration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Simulate an existing DB created before kind and resolved_at columns existed.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE attention_resolved (user_id INTEGER NOT NULL, issue TEXT NOT NULL, PRIMARY KEY(user_id,issue))`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO attention_resolved(user_id, issue) VALUES(1, 'legacy-id')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	resolved, err := st.AttentionResolved(ctx, 1)
	if err != nil || !resolved["legacy-id"] {
		t.Fatalf("legacy resolved row lost: %v %v", resolved, err)
	}

	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := st.ResolveAttention(ctx, 1, "r1", "refund", base); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveAttention(ctx, 1, "r2", "refund", base.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Duplicate resolve keeps the original timestamp.
	if err := st.ResolveAttention(ctx, 1, "r1", "refund", base.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveAttention(ctx, 1, "d1", "delivery", base.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveAttention(ctx, 2, "r3", "refund", base.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Strictly greater than base: only r2 counts for user 1 refunds.
	if n, err := st.ResolvedAttentionCount(ctx, 1, "refund", base.Unix()); err != nil || n != 1 {
		t.Fatalf("refunds after base: got %d, want 1 (err %v)", n, err)
	}
	// Before base: both r1 and r2 count, legacy row (resolved_at=0) does not.
	if n, err := st.ResolvedAttentionCount(ctx, 1, "refund", base.Add(-time.Second).Unix()); err != nil || n != 2 {
		t.Fatalf("refunds since before base: got %d, want 2 (err %v)", n, err)
	}
	if n, err := st.ResolvedAttentionCount(ctx, 1, "delivery", base.Unix()); err != nil || n != 1 {
		t.Fatalf("deliveries after base: got %d, want 1 (err %v)", n, err)
	}
	if n, err := st.ResolvedAttentionCount(ctx, 2, "refund", base.Unix()); err != nil || n != 1 {
		t.Fatalf("user 2 refunds after base: got %d, want 1 (err %v)", n, err)
	}
}
