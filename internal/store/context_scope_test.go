package store

import (
	"testing"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

func TestSessionScope_MatchesAndBoost(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	startOfToday := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	mem := model.Memory{
		Tags:      []string{"meal-memo", "chat:832881763", "date:2026-05-12"},
		CreatedAt: now.Add(-2 * time.Hour),
	}

	cases := []struct {
		name      string
		scope     *SessionScope
		wantBoost float64
	}{
		{
			name:      "nil scope = no boost",
			scope:     nil,
			wantBoost: 1.0,
		},
		{
			name:      "tag match — chat id",
			scope:     &SessionScope{Tags: []string{"chat:832881763"}, BoostFactor: 2.5},
			wantBoost: 2.5,
		},
		{
			name:      "tag match — date",
			scope:     &SessionScope{Tags: []string{"date:2026-05-12"}, BoostFactor: 3.0},
			wantBoost: 3.0,
		},
		{
			name:      "since match — created after floor",
			scope:     &SessionScope{Since: startOfToday, BoostFactor: 2.0},
			wantBoost: 2.0,
		},
		{
			name:      "no match — boost factor=2.5 stays 1.0",
			scope:     &SessionScope{Tags: []string{"chat:0"}, BoostFactor: 2.5},
			wantBoost: 1.0,
		},
		{
			name:      "since cutoff misses older memory",
			scope:     &SessionScope{Since: now.Add(-1 * time.Hour), BoostFactor: 2.5},
			wantBoost: 1.0,
		},
		{
			name:      "boost factor <= 1.0 disables even with match",
			scope:     &SessionScope{Tags: []string{"chat:832881763"}, BoostFactor: 1.0},
			wantBoost: 1.0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.scope.boost(mem)
			if got != c.wantBoost {
				t.Errorf("boost() = %v, want %v", got, c.wantBoost)
			}
		})
	}
}

func TestComputeContextScore_AppliesScopeBoost(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	mem := model.Memory{
		Tags:       []string{"chat:832881763"},
		CreatedAt:  now.Add(-1 * time.Hour),
		Kind:       "semantic",
		Importance: 0.5,
		Tier:       "ltm",
	}

	baseScore := computeContextScore(mem, 0.7, now, nil)
	scope := &SessionScope{Tags: []string{"chat:832881763"}, BoostFactor: 2.5}
	scopedScore := computeContextScore(mem, 0.7, now, scope)

	if scopedScore <= baseScore {
		t.Errorf("scopedScore (%v) must exceed baseScore (%v) when scope matches", scopedScore, baseScore)
	}
	// 2.5x boost should produce ~2.5x score
	ratio := scopedScore / baseScore
	if ratio < 2.4 || ratio > 2.6 {
		t.Errorf("expected ~2.5x boost, got %.2fx (base=%v scoped=%v)", ratio, baseScore, scopedScore)
	}
}
