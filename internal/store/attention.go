package store

import "context"

const attentionSchema = `CREATE TABLE IF NOT EXISTS attention_resolved (user_id INTEGER NOT NULL, issue TEXT NOT NULL, PRIMARY KEY(user_id,issue));`

func (s *Store) AttentionResolved(ctx context.Context, user int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT issue FROM attention_resolved WHERE user_id=?", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
func (s *Store) ResolveAttention(ctx context.Context, user int64, id string) error {
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO attention_resolved(user_id,issue) VALUES(?,?)", user, id)
	return err
}
