package store

import (
	"context"
	"fmt"
	"time"
)

// rule_events is the audit trace for decisions the store makes on its own.
// Pair rules write it today; the lifecycle (reflect promotions and demotions)
// is designed to write the same table later, which is why every row carries a
// source. A caller reviews an event with agree or disagree; disagreeing with
// an ASSERT removes the edge it wrote. Nothing on the retrieval hot path reads
// this table.

const (
	ruleEventSourcePair = "pair_rule"

	VerdictAgree    = "agree"
	VerdictDisagree = "disagree"
)

// RuleEvent is one recorded firing.
type RuleEvent struct {
	ID          string  `json:"id"`
	Source      string  `json:"source"`
	RuleID      string  `json:"rule_id"`
	FromID      string  `json:"from_id"` // older memory for pair rules
	ToID        string  `json:"to_id"`   // newer memory for pair rules
	FromKey     string  `json:"from_key,omitempty"`
	ToKey       string  `json:"to_key,omitempty"`
	Features    string  `json:"features"` // JSON as evaluated
	ActionOp    string  `json:"action_op"`
	ActionRel   string  `json:"action_rel"`
	Score       float64 `json:"score"`
	EdgeWritten bool    `json:"edge_written"`
	CreatedAt   string  `json:"created_at"`
	Reviewed    bool    `json:"reviewed"`
	Verdict     string  `json:"verdict,omitempty"`
	ReviewedBy  string  `json:"reviewed_by,omitempty"`
	ReviewedAt  string  `json:"reviewed_at,omitempty"`
}

func (s *SQLiteStore) insertRuleEvent(ctx context.Context, source string, f PairFiring) (string, error) {
	id := s.newID()
	now := s.now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO rule_events
		(id, source, rule_id, from_id, to_id, features, action_op, action_rel, score, edge_written, created_at, reviewed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, source, f.RuleID, f.OlderID, f.NewerID, featuresJSON(f.Features), f.Op, f.Rel, f.Score, boolInt(f.EdgeWritten), now)
	if err != nil {
		return "", fmt.Errorf("insert rule event: %w", err)
	}
	return id, nil
}

// ListRuleEventsParams filters the trace.
type ListRuleEventsParams struct {
	NS         string // events whose memories are in this namespace
	RuleID     string
	Unreviewed bool
	ByScore    bool // highest score first (the review queue order); default newest first
	Limit      int  // default 100
}

// ListRuleEvents returns events newest first, with memory keys resolved.
func (s *SQLiteStore) ListRuleEvents(ctx context.Context, p ListRuleEventsParams) ([]RuleEvent, error) {
	if p.Limit <= 0 {
		p.Limit = 100
	}
	q := `SELECT e.id, e.source, e.rule_id, e.from_id, e.to_id, COALESCE(mf.key,''), COALESCE(mt.key,''),
		e.features, e.action_op, e.action_rel, e.score, e.edge_written, e.created_at, e.reviewed,
		COALESCE(e.verdict,''), COALESCE(e.reviewed_by,''), COALESCE(e.reviewed_at,'')
		FROM rule_events e
		LEFT JOIN memories mf ON mf.id = e.from_id
		LEFT JOIN memories mt ON mt.id = e.to_id
		WHERE 1=1`
	var args []any
	if p.NS != "" {
		q += ` AND mf.ns = ?`
		args = append(args, p.NS)
	}
	if p.RuleID != "" {
		q += ` AND e.rule_id = ?`
		args = append(args, p.RuleID)
	}
	if p.Unreviewed {
		q += ` AND e.reviewed = 0`
	}
	if p.ByScore {
		q += ` ORDER BY e.score DESC, e.created_at DESC, e.id DESC LIMIT ?`
	} else {
		q += ` ORDER BY e.created_at DESC, e.id DESC LIMIT ?`
	}
	args = append(args, p.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleEvent
	for rows.Next() {
		var e RuleEvent
		var edge, reviewed int
		if err := rows.Scan(&e.ID, &e.Source, &e.RuleID, &e.FromID, &e.ToID, &e.FromKey, &e.ToKey,
			&e.Features, &e.ActionOp, &e.ActionRel, &e.Score, &edge, &e.CreatedAt, &reviewed,
			&e.Verdict, &e.ReviewedBy, &e.ReviewedAt); err != nil {
			return nil, err
		}
		e.EdgeWritten = edge == 1
		e.Reviewed = reviewed == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReviewRuleEvent records a verdict. Disagreeing with an event whose edge was
// written removes that edge; agreeing leaves everything as it is. Reviewing
// twice replaces the verdict, and a disagree after an agree still removes.
func (s *SQLiteStore) ReviewRuleEvent(ctx context.Context, id, verdict, by string) error {
	if s.contractReadOnly {
		return s.errContractNewer()
	}
	if verdict != VerdictAgree && verdict != VerdictDisagree {
		return fmt.Errorf("verdict must be %q or %q", VerdictAgree, VerdictDisagree)
	}
	var fromID, toID, rel string
	var edgeWritten int
	err := s.db.QueryRowContext(ctx, `SELECT from_id, to_id, action_rel, edge_written FROM rule_events WHERE id = ?`, id).
		Scan(&fromID, &toID, &rel, &edgeWritten)
	if err != nil {
		return fmt.Errorf("rule event %s: %w", id, err)
	}
	now := s.now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE rule_events SET reviewed = 1, verdict = ?, reviewed_by = ?, reviewed_at = ? WHERE id = ?`,
		verdict, by, now, id); err != nil {
		return err
	}
	// The pair-rule edge is written newer→older (from = newer = event.to_id).
	switch {
	case verdict == VerdictDisagree && edgeWritten == 1:
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_edges WHERE from_id = ? AND to_id = ? AND rel = ?`, toID, fromID, rel); err != nil {
			return err
		}
	case verdict == VerdictAgree && edgeWritten == 0:
		// Agreeing with a PROPOSAL is what creates the edge: the reviewer's
		// decision is the write. Weight is the relation's default.
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_edges (from_id, to_id, rel, weight, access_count, created_at)
			VALUES (?, ?, ?, ?, 0, ?)`, toID, fromID, rel, defaultEdgeWeight(rel), now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rule_events SET edge_written = 1 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reviewedEdgeSet returns the edges a reviewer has AGREED with, keyed as the
// edge is stored ("from|to|rel", from = newer). Read once per Context call by
// edge expansion, which lets a reviewed accompany-class edge claim reserved
// budget. This is the one place the retrieval path consults the review trace,
// and only for edges that exist; rows are few and the index makes it cheap.
func (s *SQLiteStore) reviewedEdgeSet(ctx context.Context) map[string]bool {
	set := make(map[string]bool)
	rows, err := s.db.QueryContext(ctx, `SELECT to_id, from_id, action_rel FROM rule_events WHERE verdict = ? AND edge_written = 1`, VerdictAgree)
	if err != nil {
		return set // no table yet, or unreadable: no reservations
	}
	defer rows.Close()
	for rows.Next() {
		var from, to, rel string
		if err := rows.Scan(&from, &to, &rel); err == nil {
			set[from+"|"+to+"|"+rel] = true
		}
	}
	return set
}
