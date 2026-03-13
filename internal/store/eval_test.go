package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// seedEvalStore creates a fresh store and seeds the default eval corpus.
func seedEvalStore(t *testing.T) (*SQLiteStore, map[string]string) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	ids, err := SeedStore(ctx, s, DefaultSeedCorpus())
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return s, ids
}

func extractKeys(results []SearchResult) []string {
	keys := make([]string, len(results))
	for i, r := range results {
		keys[i] = r.Key
	}
	return keys
}

func contextKeys(result *ContextResult) []string {
	keys := make([]string, len(result.Memories))
	for i, m := range result.Memories {
		keys[i] = m.Key
	}
	return keys
}

func contextHasKey(result *ContextResult, key string) bool {
	for _, m := range result.Memories {
		if m.Key == key {
			return true
		}
	}
	return false
}

// simulateSystemPrompt mimics shell's SystemPrompt(): List() from system
// namespaces, concatenate into markdown sections, enforce char budget.
// This is how shell actually builds the static system instruction.
func simulateSystemPrompt(t *testing.T, s *SQLiteStore, namespaces []string, charBudget int) string {
	t.Helper()
	ctx := context.Background()
	var sb strings.Builder
	used := 0

	for _, ns := range namespaces {
		heading := ns
		if idx := strings.LastIndex(ns, ":"); idx >= 0 && idx+1 < len(ns) {
			heading = ns[idx+1:]
		}
		heading = strings.ToUpper(heading[:1]) + heading[1:]

		memories, err := s.List(ctx, ListParams{NS: ns, Limit: 100})
		if err != nil {
			t.Logf("list %s: %v", ns, err)
			continue
		}
		if len(memories) == 0 {
			continue
		}

		section := fmt.Sprintf("## %s\n", heading)
		for _, mem := range memories {
			section += fmt.Sprintf("- %s\n", mem.Content)
		}
		section += "\n"

		if used+len(section) > charBudget {
			break
		}
		sb.WriteString(section)
		used += len(section)
	}

	return strings.TrimSpace(sb.String())
}

// ═══════════════════════════════════════════════════════════════════════
// HOT PATH: System prompt assembly (how shell does it)
//
// Shell builds the system prompt in two layers:
//   1. SystemPrompt() — List() from system namespaces (identity, capabilities)
//      → becomes the actual system instruction
//   2. InjectContext() — Context() with user message as query
//      → prepended to user message as [Background context] / [Relevant memories]
//
// This eval tests both layers as they actually work in production.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalHotPath(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	// ── Layer 1: SystemPrompt via List() ────────────────────────────

	t.Run("system_prompt_loads_identity", func(t *testing.T) {
		// Shell calls List() on system namespaces to build static system prompt.
		// All identity memories in user:prefs should appear.
		prompt := simulateSystemPrompt(t, s, []string{"user:prefs"}, 8000)

		if !strings.Contains(prompt, "concise, technical responses") {
			t.Error("identity-core content missing from system prompt")
		}
		if !strings.Contains(prompt, "direct and opinionated") {
			t.Error("personality content missing from system prompt")
		}
		if !strings.Contains(prompt, "never modify files") {
			t.Error("boundaries content missing from system prompt")
		}

		t.Logf("system prompt length: %d chars", len(prompt))
	})

	t.Run("system_prompt_loads_tools", func(t *testing.T) {
		// Shell loads tool capabilities from system:tools namespace.
		prompt := simulateSystemPrompt(t, s, []string{"system:tools"}, 8000)

		if !strings.Contains(prompt, "shell-search") {
			t.Error("tool-search missing from system prompt")
		}
		if !strings.Contains(prompt, "shell-browser") {
			t.Error("tool-browser missing from system prompt")
		}

		t.Logf("tools prompt length: %d chars", len(prompt))
	})

	t.Run("system_prompt_respects_char_budget", func(t *testing.T) {
		// Budget fits user:prefs (~1100 chars) but not user:prefs+system:tools
		prompt := simulateSystemPrompt(t, s, []string{"user:prefs", "system:tools"}, 1200)

		if len(prompt) > 1200 {
			t.Errorf("system prompt exceeded char budget: %d > 1200", len(prompt))
		}
		// user:prefs section should fit
		if len(prompt) == 0 {
			t.Error("system prompt empty even at 1200 char budget")
		}
		// tools section should NOT fit (would push over budget)
		if strings.Contains(prompt, "## Tools") {
			t.Error("expected tools section to be excluded under tight budget")
		}

		t.Logf("budget=1200 actual=%d", len(prompt))
	})

	t.Run("system_prompt_multi_namespace_ordering", func(t *testing.T) {
		// Shell iterates namespaces in config order. Identity should come
		// before tools, matching the config pattern.
		prompt := simulateSystemPrompt(t, s, []string{"user:prefs", "system:tools", "project:alpha"}, 8000)

		identityIdx := strings.Index(prompt, "## Prefs")
		toolsIdx := strings.Index(prompt, "## Tools")

		if identityIdx == -1 {
			t.Error("prefs section missing")
		}
		if toolsIdx == -1 {
			t.Error("tools section missing")
		}
		if identityIdx != -1 && toolsIdx != -1 && identityIdx > toolsIdx {
			t.Error("identity section should appear before tools section")
		}

		t.Logf("prompt sections: prefs@%d tools@%d", identityIdx, toolsIdx)
	})

	// ── Layer 2: InjectContext via Context() ────────────────────────

	t.Run("inject_context_finds_relevant_memories", func(t *testing.T) {
		// Shell calls Context() with the user's message as query.
		// "help me fix the database connection pooling issue" should
		// surface alpha's data-layer and perf-incident-jan.
		result, err := s.Context(ctx, ContextParams{
			Query:    "help me fix the database connection pooling issue",
			Budget:   1000,
			PinTiers: []string{"identity", "ltm"},
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		if !contextHasKey(result, "data-layer") {
			t.Logf("BENCHMARK: data-layer not in context for database pooling query")
		}

		tolerance := 20
		if result.Used > result.Budget+tolerance {
			t.Errorf("budget exceeded: %d > %d", result.Used, result.Budget)
		}

		t.Logf("budget=%d used=%d memories=%d keys=%v",
			result.Budget, result.Used, len(result.Memories), contextKeys(result))
	})

	t.Run("inject_context_tight_budget_prioritizes", func(t *testing.T) {
		// Under tight budget, pinned identity/ltm fill first,
		// then only the most relevant search results fit.
		result, err := s.Context(ctx, ContextParams{
			Query:    "tell me about the deployment process",
			Budget:   300,
			PinTiers: []string{"identity", "ltm"},
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		tolerance := 20
		if result.Used > result.Budget+tolerance {
			t.Errorf("budget exceeded: %d > %d", result.Used, result.Budget)
		}
		if len(result.Memories) == 0 {
			t.Error("no memories returned at 300 token budget")
		}

		t.Logf("budget=%d used=%d memories=%d keys=%v",
			result.Budget, result.Used, len(result.Memories), contextKeys(result))
	})

	t.Run("inject_context_ns_scoped", func(t *testing.T) {
		// Shell scopes per-chat context to chat namespace.
		// Simulating with project:alpha — beta should not leak.
		result, err := s.Context(ctx, ContextParams{
			NS:       "project:alpha",
			Query:    "database issues",
			Budget:   2000,
			PinTiers: []string{"identity", "ltm"},
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		for _, m := range result.Memories {
			if m.NS == "project:beta" {
				t.Errorf("beta memory %q leaked into alpha-scoped context", m.Key)
			}
		}

		t.Logf("budget=%d used=%d memories=%d", result.Budget, result.Used, len(result.Memories))
	})
}

// ═══════════════════════════════════════════════════════════════════════
// COLD PATH: Mid-conversation recall
//
// During a conversation, the agent calls ghost_search (or shell calls
// Context/Search internally) to recall memories triggered by the user's
// message. The query is natural language, not keywords.
//
// Three difficulty tiers:
//   - KEYWORD: literal term overlap (baseline, must pass)
//   - SEMANTIC: meaning overlap, few/no shared keywords (needs embeddings)
//   - ADVERSARIAL: distractors share keywords but wrong meaning
// ═══════════════════════════════════════════════════════════════════════

func TestEvalColdPath(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		difficulty string
		query      string
		ns         string
		wantKeys   []string
		notKeys    []string
		topK       int
	}{
		// ── Keyword (baseline) ──────────────────────────────────────
		{
			name: "keyword/postgresql_direct", difficulty: "keyword",
			query: "PostgreSQL pgvector", wantKeys: []string{"data-layer"}, topK: 3,
		},
		{
			name: "keyword/jwt_auth", difficulty: "keyword",
			query: "JWT refresh token", wantKeys: []string{"auth-flow"}, topK: 3,
		},
		{
			name: "keyword/ns_isolation", difficulty: "keyword",
			query: "pool exhaustion", ns: "project:alpha",
			wantKeys: []string{"perf-incident-jan"},
			notKeys:  []string{"beta-pool-fix", "beta-pool-incident"},
			topK:     5,
		},

		// ── Semantic (conversational recall) ────────────────────────
		{
			// "how do we get code to production?" — content says "merge PR", "ArgoCD"
			name: "semantic/how_to_ship", difficulty: "semantic",
			query: "how do we get code to production", wantKeys: []string{"ship-process"}, topK: 5,
		},
		{
			// "what IDE does the user prefer?" — content says "VS Code", "vim"
			name: "semantic/editor_preference", difficulty: "semantic",
			query: "what IDE setup does the user prefer", wantKeys: []string{"editor-setup"}, topK: 5,
		},
		{
			// "how should I handle errors?" — content says "AppError", "Result[T]"
			name: "semantic/error_pattern", difficulty: "semantic",
			query: "how should I handle errors in the codebase", wantKeys: []string{"fault-handling"}, topK: 5,
		},
		{
			// "when can I reach the user?" — content says "10am-6pm PT"
			name: "semantic/user_availability", difficulty: "semantic",
			query: "when can I reach the user", wantKeys: []string{"schedule"}, topK: 5,
		},
		{
			// "how do I search the web?" — content says "shell-search", "Brave"
			name: "semantic/tool_lookup", difficulty: "semantic",
			query: "how do I search the web", wantKeys: []string{"tool-search"}, topK: 5,
		},
		{
			// "how did this project start?" — content says "hackathon prototype"
			name: "semantic/project_origin", difficulty: "semantic",
			query: "how did this project start", wantKeys: []string{"lore-origin"}, topK: 5,
		},

		// ── Adversarial (disambiguation) ────────────────────────────
		{
			// "pool exhaustion" in both alpha (DB) and beta (thread pool).
			name: "adversarial/pool_disambiguation", difficulty: "adversarial",
			query: "database connection pool exhaustion", wantKeys: []string{"perf-incident-jan"}, topK: 3,
		},
		{
			// Recent incident should rank above old one.
			name: "adversarial/recent_over_old", difficulty: "adversarial",
			query: "the most recent production incident", wantKeys: []string{"perf-incident-mar"}, topK: 3,
		},
		{
			// Scoped: alpha's latency issues, no beta leakage.
			name: "adversarial/latency_cross_ns", difficulty: "adversarial",
			query: "latency issues", ns: "project:alpha",
			wantKeys: []string{"perf-incident-mar"},
			notKeys:  []string{"beta-pool-incident"},
			topK:     5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := SearchParams{Query: tt.query, Limit: tt.topK}
			if tt.ns != "" {
				params.NS = tt.ns
			}
			results, err := s.Search(ctx, params)
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			retrieved := extractKeys(results)
			relevant := make(map[string]bool, len(tt.wantKeys))
			for _, k := range tt.wantKeys {
				relevant[k] = true
			}

			p := PrecisionAtK(retrieved, tt.wantKeys, tt.topK)
			r := RecallAtK(retrieved, tt.wantKeys, tt.topK)
			mrr := MRR(retrieved, relevant)

			t.Logf("[%s] retrieved=%v  P@%d=%.2f  R@%d=%.2f  MRR=%.2f",
				tt.difficulty, retrieved, tt.topK, p, tt.topK, r, mrr)

			if tt.difficulty == "keyword" {
				if r < 1.0 && len(tt.wantKeys) > 0 {
					t.Errorf("recall@%d = %.2f, want 1.0", tt.topK, r)
				}
			} else if r < 1.0 {
				t.Logf("BENCHMARK: %s recall=%.2f", tt.name, r)
			}

			retrievedSet := make(map[string]bool, len(retrieved))
			for _, k := range retrieved {
				retrievedSet[k] = true
			}
			for _, nk := range tt.notKeys {
				if retrievedSet[nk] {
					t.Errorf("unexpected key %q in results %v", nk, retrieved)
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// MEMORY CORRECTION: Update-then-retrieve
//
// User: "actually the ORM migration is done, we finished it yesterday."
// Agent calls Put() with the same key → creates v2. All subsequent
// Get/Search/Context must return the corrected version.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalMemoryCorrection(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	t.Run("v1_searchable_before_update", func(t *testing.T) {
		results, err := s.Search(ctx, SearchParams{Query: "GORM sqlc migration", Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		found := false
		for _, r := range results {
			if r.Key == "legacy-orm" {
				found = true
				if r.Version != 1 {
					t.Errorf("expected version 1, got %d", r.Version)
				}
			}
		}
		if !found {
			t.Error("legacy-orm not found before update")
		}
	})

	correctedContent := "The sqlc migration is complete as of 2024-03-20. All user service endpoints now use sqlc. GORM has been fully removed. N+1 query issues are resolved."
	updated, err := s.Put(ctx, PutParams{
		NS: "project:alpha", Key: "legacy-orm", Content: correctedContent,
		Kind: "semantic", Priority: "normal", Importance: 0.6,
	})
	if err != nil {
		t.Fatalf("put correction: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("expected version 2, got %d", updated.Version)
	}

	t.Run("get_returns_v2", func(t *testing.T) {
		mems, err := s.Get(ctx, GetParams{NS: "project:alpha", Key: "legacy-orm"})
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(mems) == 0 {
			t.Fatal("get returned nothing")
		}
		if mems[0].Version != 2 {
			t.Errorf("got version %d, want 2", mems[0].Version)
		}
		if mems[0].Content != correctedContent {
			t.Error("got stale content")
		}
	})

	t.Run("search_returns_v2_not_stale", func(t *testing.T) {
		results, err := s.Search(ctx, SearchParams{Query: "sqlc migration GORM", Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, r := range results {
			if r.Key == "legacy-orm" {
				if r.Version != 2 {
					t.Errorf("search returned stale version %d", r.Version)
				}
				return
			}
		}
		t.Error("legacy-orm not in search results after correction")
	})

	t.Run("search_finds_new_terms", func(t *testing.T) {
		// Corrected content has "complete" and "resolved" — new terms.
		results, err := s.Search(ctx, SearchParams{Query: "sqlc migration complete resolved", Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		found := false
		for _, r := range results {
			if r.Key == "legacy-orm" && r.Version == 2 {
				found = true
			}
		}
		if !found {
			t.Logf("BENCHMARK: corrected memory not found via new terms")
		}
	})

	t.Run("context_uses_corrected_content", func(t *testing.T) {
		result, err := s.Context(ctx, ContextParams{
			NS: "project:alpha", Query: "ORM migration status", Budget: 2000,
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}
		for _, m := range result.Memories {
			if m.Key == "legacy-orm" {
				if m.Content != correctedContent {
					t.Error("context has stale content after correction")
				}
				return
			}
		}
		t.Logf("BENCHMARK: corrected legacy-orm not in context")
	})

	t.Run("history_preserves_both_versions", func(t *testing.T) {
		mems, err := s.History(ctx, HistoryParams{NS: "project:alpha", Key: "legacy-orm"})
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(mems) < 2 {
			t.Errorf("expected 2+ versions, got %d", len(mems))
		}
		versions := map[int]bool{}
		for _, m := range mems {
			versions[m.Version] = true
		}
		if !versions[1] || !versions[2] {
			t.Errorf("expected versions 1 and 2, got %v", versions)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════
// REFLECT LIFECYCLE: Memory hygiene
// ═══════════════════════════════════════════════════════════════════════

func TestEvalReflectLifecycle(t *testing.T) {
	s, ids := seedEvalStore(t)
	ctx := context.Background()

	var preImportance float64
	s.db.QueryRow(`SELECT importance FROM memories WHERE id = ?`, ids["reflect-decay-target"]).Scan(&preImportance)

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	t.Logf("reflect: evaluated=%d applied=%d decayed=%d promoted=%d demoted=%d deleted=%d",
		result.MemoriesEvaluated, result.RulesApplied,
		result.Decayed, result.Promoted, result.Demoted, result.Deleted)

	t.Run("decay_old_stm", func(t *testing.T) {
		var importance float64
		s.db.QueryRow(`SELECT importance FROM memories WHERE id = ?`,
			ids["reflect-decay-target"]).Scan(&importance)
		if importance >= preImportance {
			t.Errorf("expected importance < %.2f, got %.2f", preImportance, importance)
		}
		t.Logf("decay: %.3f → %.3f (factor=0.95)", preImportance, importance)
	})

	t.Run("promote_accessed_stm", func(t *testing.T) {
		var tier string
		s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, ids["reflect-promote-target"]).Scan(&tier)
		if tier != "ltm" {
			t.Errorf("expected 'ltm', got %q", tier)
		}
	})

	t.Run("demote_stale_ltm", func(t *testing.T) {
		var tier string
		s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, ids["reflect-demote-target"]).Scan(&tier)
		if tier != "dormant" {
			t.Errorf("expected 'dormant', got %q", tier)
		}
	})

	t.Run("prune_low_utility", func(t *testing.T) {
		var deletedAt *string
		s.db.QueryRow(`SELECT deleted_at FROM memories WHERE id = ?`, ids["reflect-prune-target"]).Scan(&deletedAt)
		if deletedAt == nil {
			t.Error("expected soft-delete")
		}
	})

	t.Run("pinned_protected", func(t *testing.T) {
		var tier string
		var pinned int
		var importance float64
		var deletedAt *string
		s.db.QueryRow(`SELECT tier, pinned, importance, deleted_at FROM memories WHERE id = ?`,
			ids["reflect-identity-safe"]).Scan(&tier, &pinned, &importance, &deletedAt)
		if tier != "ltm" {
			t.Errorf("expected 'ltm', got %q", tier)
		}
		if pinned != 1 {
			t.Errorf("expected pinned=1, got %d", pinned)
		}
		if importance != 1.0 {
			t.Errorf("expected importance 1.0, got %.2f", importance)
		}
		if deletedAt != nil {
			t.Error("pinned memory must not be deleted")
		}
	})

	t.Run("pruned_excluded_from_search", func(t *testing.T) {
		results, _ := s.Search(ctx, SearchParams{Query: "memory accessed useful utility", Limit: 10})
		for _, r := range results {
			if r.Key == "reflect-prune-target" {
				t.Error("pruned memory in search results")
			}
		}
	})

	t.Run("sensory_attended_promoted_to_stm", func(t *testing.T) {
		var tier string
		s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, ids["sensory-attended"]).Scan(&tier)
		if tier != "stm" {
			t.Errorf("expected attended sensory promoted to 'stm', got %q", tier)
		}
	})

	t.Run("sensory_unattended_deleted", func(t *testing.T) {
		var deletedAt *string
		s.db.QueryRow(`SELECT deleted_at FROM memories WHERE id = ?`, ids["sensory-unattended"]).Scan(&deletedAt)
		if deletedAt == nil {
			t.Error("expected unattended sensory memory (>4h) to be deleted")
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════
// NO-RESULT PRECISION: Queries about things not in memory
//
// When the agent asks about something genuinely absent, the system
// should return nothing useful — not misleading near-matches. An agent
// that gets plausible-but-wrong results will confidently state wrong facts.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalNoResultPrecision(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		query string
		// All results are "wrong" for these queries — we measure how many come back
		// and whether any are dangerously misleading
		dangerousKeys []string // keys that would cause incorrect agent behavior
	}{
		{
			// Nothing in seed corpus about Kubernetes
			name:          "absent_kubernetes",
			query:         "Kubernetes pod deployment configuration",
			dangerousKeys: []string{"ship-process"}, // deploy != k8s deploy
		},
		{
			// Nothing about MongoDB — alpha uses PostgreSQL, beta uses S3/DynamoDB
			name:          "absent_mongodb",
			query:         "MongoDB replica set configuration",
			dangerousKeys: []string{"data-layer"}, // PostgreSQL != MongoDB
		},
		{
			// Nothing about Python — projects use Go
			name:          "absent_python",
			query:         "Python virtual environment setup",
			dangerousKeys: []string{"go-version"}, // Go != Python
		},
		{
			// Nothing about CI — ship-process mentions CI but not CI config
			name:          "absent_ci_config",
			query:         "GitHub Actions CI workflow YAML configuration",
			dangerousKeys: nil,
		},
		{
			// Completely unrelated domain
			name:          "absent_cooking",
			query:         "best recipe for chocolate soufflé",
			dangerousKeys: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := s.Search(ctx, SearchParams{Query: tt.query, Limit: 5})
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			retrieved := extractKeys(results)
			t.Logf("no-result: query=%q retrieved=%v (want: nothing relevant)", tt.query, retrieved)

			// Check for dangerously misleading results in top-3
			for _, dk := range tt.dangerousKeys {
				for i, k := range retrieved {
					if k == dk && i < 3 {
						t.Logf("BENCHMARK: dangerous false positive %q at rank %d for %q", dk, i+1, tt.query)
					}
				}
			}

			// Track how many results come back (ideal: 0 or low-relevance)
			if len(results) > 0 {
				t.Logf("BENCHMARK: %d results for absent query %q — ideally 0", len(results), tt.query)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// VAGUE QUERIES: Underspecified searches that agents actually make
//
// Real agent recall is often imprecise: "the config thing", "that issue",
// "our testing approach". Tests whether the system returns useful results
// or garbage under ambiguity.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalVagueQueries(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		query      string
		acceptable []string // any of these would be a reasonable answer
		topK       int
	}{
		{
			// Very vague — "the config" could mean many things
			name:       "the_config",
			query:      "the config",
			acceptable: []string{"alerts-config", "editor-setup"},
			topK:       5,
		},
		{
			// "that issue" — could be any incident
			name:       "that_issue",
			query:      "that issue we had",
			acceptable: []string{"perf-incident-jan", "perf-incident-mar", "beta-pool-fix", "beta-pool-incident"},
			topK:       5,
		},
		{
			// "our testing approach" — vague but has one clear target
			name:       "testing_approach",
			query:      "how do we test things",
			acceptable: []string{"test-pyramid"},
			topK:       5,
		},
		{
			// "the database stuff" — multiple valid answers
			name:       "database_stuff",
			query:      "the database stuff",
			acceptable: []string{"data-layer", "perf-incident-jan", "perf-incident-mar", "legacy-orm"},
			topK:       5,
		},
		{
			// "user info" — could be preferences, schedule, identity
			name:       "user_info",
			query:      "user info",
			acceptable: []string{"identity-core", "personality", "editor-setup", "comm-style", "schedule", "git-prefs"},
			topK:       5,
		},
		{
			// Single word query
			name:       "single_word_deploy",
			query:      "deploy",
			acceptable: []string{"ship-process", "deploy-yesterday", "deploy-last-week", "deploy-last-month"},
			topK:       5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := s.Search(ctx, SearchParams{Query: tt.query, Limit: tt.topK})
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			retrieved := extractKeys(results)
			acceptSet := make(map[string]bool, len(tt.acceptable))
			for _, k := range tt.acceptable {
				acceptSet[k] = true
			}

			// Count how many of the top results are at least "acceptable"
			hits := 0
			for _, k := range retrieved {
				if acceptSet[k] {
					hits++
				}
			}

			precision := 0.0
			if len(retrieved) > 0 {
				precision = float64(hits) / float64(len(retrieved))
			}
			anyHit := hits > 0

			t.Logf("vague: query=%q retrieved=%v acceptable=%v precision=%.2f",
				tt.query, retrieved, tt.acceptable, precision)

			if !anyHit {
				t.Logf("BENCHMARK: vague/%s — no acceptable results in top-%d", tt.name, tt.topK)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// UTILITY FEEDBACK: UtilityInc affects future retrieval
//
// Agent marks a memory as useful. On the next retrieval, that memory
// should rank higher due to the utility signal feeding into context
// assembly scoring (accessFreq component).
// ═══════════════════════════════════════════════════════════════════════

func TestEvalUtilityFeedback(t *testing.T) {
	s, ids := seedEvalStore(t)
	ctx := context.Background()

	t.Run("utility_inc_boosts_context_rank", func(t *testing.T) {
		// Get initial context ranking for a query that returns multiple candidates
		query := "project architecture and database"
		baseline, err := s.Context(ctx, ContextParams{Query: query, Budget: 2000})
		if err != nil {
			t.Fatalf("context: %v", err)
		}
		baselineKeys := contextKeys(baseline)

		// Pick a memory that appeared but not at rank 1 — boost it via ID lookup
		boostKey := ""
		for i, m := range baseline.Memories {
			if i > 1 { // skip first couple (likely pinned)
				boostKey = m.Key
				break
			}
		}
		if boostKey == "" || ids[boostKey] == "" {
			t.Skip("not enough memories to test boost")
		}
		boostID := ids[boostKey]

		// Mark it as useful multiple times
		for i := 0; i < 10; i++ {
			if err := s.UtilityInc(ctx, boostID); err != nil {
				t.Fatalf("utility inc: %v", err)
			}
		}

		// Re-query — boosted memory should rank higher
		after, err := s.Context(ctx, ContextParams{Query: query, Budget: 2000})
		if err != nil {
			t.Fatalf("context: %v", err)
		}
		afterKeys := contextKeys(after)

		baselineRank := -1
		afterRank := -1
		for i, k := range baselineKeys {
			if k == boostKey {
				baselineRank = i
			}
		}
		for i, k := range afterKeys {
			if k == boostKey {
				afterRank = i
			}
		}

		t.Logf("utility boost: %q rank %d -> %d (utility_count=10)", boostKey, baselineRank, afterRank)
		if afterRank >= baselineRank && baselineRank >= 0 {
			t.Logf("BENCHMARK: utility_inc did not improve rank for %q (%d -> %d)", boostKey, baselineRank, afterRank)
		}
	})

	t.Run("high_access_low_utility_gets_pruned", func(t *testing.T) {
		// reflect-prune-target has access=10, utility=1 (ratio=0.1 < 0.2)
		// After reflect, it should be soft-deleted
		// This is already tested in TestEvalReflectLifecycle but we verify
		// that the utility ratio mechanism works end-to-end
		var deletedAt *string
		// Run reflect first
		s.Reflect(ctx, ReflectParams{})
		s.db.QueryRow(`SELECT deleted_at FROM memories WHERE id = ?`, ids["reflect-prune-target"]).Scan(&deletedAt)
		if deletedAt == nil {
			t.Error("memory with 10 accesses but 1 utility should be pruned")
		}

		// Also verify it's excluded from search
		results, _ := s.Search(ctx, SearchParams{Query: "surfaced often never useful", Limit: 10})
		for _, r := range results {
			if r.Key == "reflect-prune-target" {
				t.Error("pruned memory still appears in search")
			}
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════
// ACCESS-DRIVEN PROMOTION: Frequently accessed STM -> LTM
//
// When an agent repeatedly retrieves a memory (via Get/Search/Context),
// access_count grows. Reflect's promote rule (AccessGT: 3, AgeGTHours: 24)
// should promote it from STM to LTM. This tests the full loop:
// store -> access repeatedly -> reflect -> verify promotion.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalAccessPromotion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create a fresh STM memory
	mem, err := s.Put(ctx, PutParams{
		NS:         "project:test",
		Key:        "useful-pattern",
		Content:    "When writing table-driven tests in Go, use t.Run for subtests and t.Helper in shared setup functions.",
		Kind:       "procedural",
		Priority:   "normal",
		Importance: 0.6,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Verify initial state: tier=stm (default), access_count=0
	var tier string
	var accessCount int
	s.db.QueryRow(`SELECT tier, access_count FROM memories WHERE id = ?`, mem.ID).Scan(&tier, &accessCount)
	if tier != "stm" {
		t.Fatalf("expected initial tier=stm, got %q", tier)
	}
	if accessCount != 0 {
		t.Fatalf("expected initial access_count=0, got %d", accessCount)
	}

	// Simulate agent accessing this memory 12 times via Get (promote threshold is >10)
	for i := 0; i < 12; i++ {
		_, err := s.Get(ctx, GetParams{NS: "project:test", Key: "useful-pattern"})
		if err != nil {
			t.Fatalf("get #%d: %v", i, err)
		}
	}

	// Verify access count incremented
	s.db.QueryRow(`SELECT access_count FROM memories WHERE id = ?`, mem.ID).Scan(&accessCount)
	if accessCount < 11 {
		t.Errorf("expected access_count >= 11 after 12 gets, got %d", accessCount)
	}
	t.Logf("after 12 gets: access_count=%d", accessCount)

	// Backdate to >24h old (promote rule requires AgeGTHours: 24)
	backdated := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, backdated, mem.ID)

	// Run reflect
	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	t.Logf("reflect: promoted=%d", result.Promoted)

	// Verify promotion to LTM
	s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, mem.ID).Scan(&tier)
	if tier != "ltm" {
		t.Errorf("expected tier=ltm after promotion, got %q", tier)
	}

	// Run reflect again — should NOT demote (it's now LTM with high access)
	s.Reflect(ctx, ReflectParams{})
	s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, mem.ID).Scan(&tier)
	if tier != "ltm" {
		t.Errorf("expected tier=ltm to persist, got %q (double-reflect changed it)", tier)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// REFLECT RULE THRESHOLDS: Verify updated thresholds work correctly
//
// Tests the three rule changes:
// 1. Promotion threshold raised from >3 to >10 (5 accesses should NOT promote)
// 2. Decay rule fires on moderate-access STM (access_count < 10, age > 48h)
// 3. Demote uses last_accessed_at, not created_at (high access but stale)
// ═══════════════════════════════════════════════════════════════════════

func TestEvalReflectRuleThresholds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	t.Run("promotion_below_threshold", func(t *testing.T) {
		// STM memory with 5 accesses (old threshold was >3, new is >10)
		// Should NOT be promoted to LTM
		mem, err := s.Put(ctx, PutParams{
			NS: "project:test", Key: "should-not-promote",
			Content: "Memory with moderate access that should stay in STM.",
			Kind: "semantic", Priority: "normal", Importance: 0.6,
		})
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		// Set access_count=5, backdate >24h
		s.db.ExecContext(ctx, `UPDATE memories SET access_count = 5, created_at = ? WHERE id = ?`,
			time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339), mem.ID)

		s.Reflect(ctx, ReflectParams{NS: "project:test"})

		var tier string
		s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, mem.ID).Scan(&tier)
		if tier == "ltm" {
			t.Errorf("memory with access_count=5 should NOT be promoted to LTM (threshold is >10), got tier=%s", tier)
		}
	})

	t.Run("decay_fires_moderate_access", func(t *testing.T) {
		// STM memory >48h old with access_count=5 (< 10 threshold)
		// Should have importance decayed
		mem, err := s.Put(ctx, PutParams{
			NS: "project:test", Key: "should-decay",
			Content: "STM memory that should decay due to low access relative to threshold.",
			Kind: "semantic", Priority: "normal", Importance: 0.8,
		})
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		// Set access_count=5, backdate >48h
		s.db.ExecContext(ctx, `UPDATE memories SET access_count = 5, created_at = ? WHERE id = ?`,
			time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339), mem.ID)

		var preDec float64
		s.db.QueryRow(`SELECT importance FROM memories WHERE id = ?`, mem.ID).Scan(&preDec)

		s.Reflect(ctx, ReflectParams{NS: "project:test"})

		var postDec float64
		s.db.QueryRow(`SELECT importance FROM memories WHERE id = ?`, mem.ID).Scan(&postDec)
		if postDec >= preDec {
			t.Errorf("expected importance to decay: before=%.3f, after=%.3f", preDec, postDec)
		}
		t.Logf("decay: %.3f → %.3f", preDec, postDec)
	})

	t.Run("demote_uses_last_accessed_at", func(t *testing.T) {
		// LTM memory with high access_count but last_accessed_at > 168h ago
		// Should be demoted to dormant (new rule uses UnaccessedGTHours, not AccessLT)
		mem, err := s.Put(ctx, PutParams{
			NS: "project:test", Key: "stale-ltm-high-access",
			Content: "LTM memory accessed many times historically but not recently.",
			Kind: "semantic", Priority: "normal", Importance: 0.7, Tier: "ltm",
		})
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		s.db.ExecContext(ctx, `UPDATE memories SET tier = 'ltm', access_count = 50, last_accessed_at = ? WHERE id = ?`,
			time.Now().Add(-200*time.Hour).UTC().Format(time.RFC3339), mem.ID)

		s.Reflect(ctx, ReflectParams{NS: "project:test"})

		var tier string
		s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, mem.ID).Scan(&tier)
		if tier != "dormant" {
			t.Errorf("LTM memory with last_accessed_at >168h ago should be demoted, got tier=%s", tier)
		}
	})

	t.Run("demote_spares_recently_accessed", func(t *testing.T) {
		// LTM memory with last_accessed_at recently (< 168h ago)
		// Should NOT be demoted even if created_at is very old
		mem, err := s.Put(ctx, PutParams{
			NS: "project:test", Key: "active-ltm",
			Content: "LTM memory that was accessed recently and should stay.",
			Kind: "semantic", Priority: "normal", Importance: 0.7, Tier: "ltm",
		})
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		s.db.ExecContext(ctx, `UPDATE memories SET tier = 'ltm', access_count = 2, created_at = ?, last_accessed_at = ? WHERE id = ?`,
			time.Now().Add(-500*time.Hour).UTC().Format(time.RFC3339),
			time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339),
			mem.ID)

		s.Reflect(ctx, ReflectParams{NS: "project:test"})

		var tier string
		s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, mem.ID).Scan(&tier)
		if tier != "ltm" {
			t.Errorf("recently accessed LTM memory should NOT be demoted, got tier=%s", tier)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════
// IDEMPOTENT RE-STORAGE / DEDUPLICATION
//
// Agent stores the same fact multiple times across sessions. Tests:
// 1. Same key + same content → creates new version (current behavior)
// 2. Search doesn't return duplicate entries for the same key
// 3. Similar content under different keys → both appear in search
// ═══════════════════════════════════════════════════════════════════════

func TestEvalIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	content := "User prefers dark mode in all applications and editors."

	t.Run("same_key_same_content_versions", func(t *testing.T) {
		// Put the same content twice with the same key
		v1, err := s.Put(ctx, PutParams{
			NS: "user:prefs", Key: "theme", Content: content,
			Kind: "semantic", Priority: "normal", Importance: 0.7,
		})
		if err != nil {
			t.Fatalf("put v1: %v", err)
		}
		if v1.Version != 1 {
			t.Errorf("expected v1, got %d", v1.Version)
		}

		v2, err := s.Put(ctx, PutParams{
			NS: "user:prefs", Key: "theme", Content: content,
			Kind: "semantic", Priority: "normal", Importance: 0.7,
		})
		if err != nil {
			t.Fatalf("put v2: %v", err)
		}
		if v2.Version != 2 {
			t.Errorf("expected v2, got %d", v2.Version)
		}

		// Get should return only latest
		mems, _ := s.Get(ctx, GetParams{NS: "user:prefs", Key: "theme"})
		if len(mems) != 1 {
			t.Errorf("expected 1 memory from Get, got %d", len(mems))
		}
		if mems[0].Version != 2 {
			t.Errorf("expected version 2, got %d", mems[0].Version)
		}
	})

	t.Run("search_no_duplicate_keys", func(t *testing.T) {
		// Search should not return both v1 and v2 of the same key
		results, err := s.Search(ctx, SearchParams{Query: "dark mode theme preference", Limit: 10})
		if err != nil {
			t.Fatalf("search: %v", err)
		}

		keyCounts := make(map[string]int)
		for _, r := range results {
			keyCounts[r.Key]++
		}
		for key, count := range keyCounts {
			if count > 1 {
				t.Errorf("duplicate key %q appeared %d times in search results", key, count)
			}
		}
		t.Logf("search results: %v", extractKeys(results))
	})

	t.Run("similar_content_different_keys", func(t *testing.T) {
		// Store similar (but not identical) content under different keys
		_, err := s.Put(ctx, PutParams{
			NS: "user:prefs", Key: "editor-theme", Content: "VS Code dark theme with Monokai color scheme.",
			Kind: "semantic", Priority: "normal", Importance: 0.6,
		})
		if err != nil {
			t.Fatalf("put editor-theme: %v", err)
		}

		// Both should appear in search — different keys are legitimate
		results, err := s.Search(ctx, SearchParams{Query: "dark mode theme", Limit: 10})
		if err != nil {
			t.Fatalf("search: %v", err)
		}

		retrieved := extractKeys(results)
		themeFound := false
		editorThemeFound := false
		for _, k := range retrieved {
			if k == "theme" {
				themeFound = true
			}
			if k == "editor-theme" {
				editorThemeFound = true
			}
		}
		t.Logf("dedup search: retrieved=%v theme=%v editor-theme=%v", retrieved, themeFound, editorThemeFound)

		if !themeFound {
			t.Logf("BENCHMARK: 'theme' not found in search for 'dark mode theme'")
		}
		if !editorThemeFound {
			t.Logf("BENCHMARK: 'editor-theme' not found in search for 'dark mode theme'")
		}
	})

	t.Run("context_no_duplicate_keys", func(t *testing.T) {
		// Context should also not return duplicate keys
		result, err := s.Context(ctx, ContextParams{
			Query: "dark mode preferences", Budget: 2000,
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		keyCounts := make(map[string]int)
		for _, m := range result.Memories {
			keyCounts[m.Key]++
		}
		for key, count := range keyCounts {
			if count > 1 {
				t.Errorf("duplicate key %q appeared %d times in context", key, count)
			}
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════
// CONTEXT EFFICIENCY: Information density within budget
//
// Tests whether context assembly maximizes useful information per token.
// Given a fixed budget, the system should pack the most relevant memories
// rather than wasting space on low-value pinned memories while excluding
// the one memory that actually answers the question.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalContextEfficiency(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	t.Run("relevant_memory_not_crowded_out", func(t *testing.T) {
		// "pgvector index corruption" has one perfect match (perf-incident-mar)
		// Under a tight budget, pinned identity memories should not crowd it out
		result, err := s.Context(ctx, ContextParams{
			Query:    "pgvector index corruption vacuum",
			Budget:   200,
			PinTiers: []string{"identity"},
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		keys := contextKeys(result)
		t.Logf("efficiency: budget=200 keys=%v", keys)

		hasTarget := contextHasKey(result, "perf-incident-mar")
		if !hasTarget {
			t.Logf("BENCHMARK: perf-incident-mar crowded out by pinned memories at budget=200")
		}
	})

	t.Run("budget_utilization_not_wasteful", func(t *testing.T) {
		// With a generous budget, utilization should be reasonable
		result, err := s.Context(ctx, ContextParams{
			Query:    "project architecture and deployment",
			Budget:   1000,
			PinTiers: []string{"identity", "ltm"},
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		utilization := float64(result.Used) / float64(result.Budget)
		t.Logf("efficiency: budget=%d used=%d util=%.2f memories=%d",
			result.Budget, result.Used, utilization, len(result.Memories))

		if utilization < 0.5 {
			t.Logf("BENCHMARK: budget utilization %.2f < 0.5 — system is wasting budget", utilization)
		}
	})

	t.Run("no_pin_budget_goes_to_search", func(t *testing.T) {
		// Without pinned tiers, the entire budget should go to search-relevant memories
		withPin, err := s.Context(ctx, ContextParams{
			Query:    "JWT authentication rate limiting",
			Budget:   500,
			PinTiers: []string{"identity"},
		})
		if err != nil {
			t.Fatalf("context with pin: %v", err)
		}

		withoutPin, err := s.Context(ctx, ContextParams{
			Query:  "JWT authentication rate limiting",
			Budget: 500,
		})
		if err != nil {
			t.Fatalf("context without pin: %v", err)
		}

		pinKeys := contextKeys(withPin)
		noPinKeys := contextKeys(withoutPin)
		t.Logf("with pin: %v", pinKeys)
		t.Logf("without pin: %v", noPinKeys)

		// auth-flow should appear in both
		authInPin := contextHasKey(withPin, "auth-flow")
		authNoPin := contextHasKey(withoutPin, "auth-flow")
		if !authInPin && !authNoPin {
			t.Logf("BENCHMARK: auth-flow missing from both pinned and unpinned context")
		}
		// Without pin, there should be more room for search-relevant results
		if len(noPinKeys) < len(pinKeys) {
			t.Logf("BENCHMARK: removing pins didn't free budget for more search results (%d vs %d)", len(noPinKeys), len(pinKeys))
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════
// MULTI-HOP RECALL: Questions requiring info from 2+ memories
//
// Real agent scenario: user asks a question whose answer isn't in any
// single memory, but can be composed by chaining facts across memories.
// We test whether the retrieval surfaces ALL required pieces.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalMultiHop(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		query    string
		ns       string
		wantKeys []string // ALL must be in top-K for the answer to be composable
		topK     int
	}{
		{
			// "What database does the service behind /api/v2/users use?"
			// Requires: atlas-routing (routes /users → user service)
			//         + user-service-arch (user service → PostgreSQL via PgBouncer)
			//         + data-layer (PostgreSQL 15 + pgvector details)
			name:     "gateway_to_database",
			query:    "what database does the API gateway route user requests to",
			wantKeys: []string{"atlas-routing", "user-service-arch", "data-layer"},
			topK:     5,
		},
		{
			// "How does search work end-to-end?"
			// Requires: atlas-routing (gateway routes /search)
			//         + search-service-arch (uses pgvector, read replica)
			//         + data-layer (PostgreSQL + pgvector extension)
			name:     "search_end_to_end",
			query:    "how does the search feature work from the API layer to the database",
			wantKeys: []string{"atlas-routing", "search-service-arch", "data-layer"},
			topK:     5,
		},
		{
			// "What changed in the most recent deploy and how do deploys work?"
			// Requires: deploy-yesterday (what changed)
			//         + ship-process (how deploys work: ArgoCD, ECS)
			name:     "deploy_what_and_how",
			query:    "what was in the latest deployment and how does our deploy pipeline work",
			wantKeys: []string{"deploy-yesterday", "ship-process"},
			topK:     5,
		},
		{
			// "After the search latency incident, what architectural choices prevent it?"
			// Requires: perf-incident-mar (the incident: pgvector index corruption)
			//         + search-service-arch (off-peak rebuilds, read replica)
			name:     "incident_plus_architecture",
			query:    "what caused the search latency incident and what safeguards exist",
			wantKeys: []string{"perf-incident-mar", "search-service-arch"},
			topK:     5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := SearchParams{Query: tt.query, Limit: tt.topK}
			if tt.ns != "" {
				params.NS = tt.ns
			}
			results, err := s.Search(ctx, params)
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			retrieved := extractKeys(results)
			retrievedSet := make(map[string]bool, len(retrieved))
			for _, k := range retrieved {
				retrievedSet[k] = true
			}

			found := 0
			for _, k := range tt.wantKeys {
				if retrievedSet[k] {
					found++
				}
			}

			recall := float64(found) / float64(len(tt.wantKeys))
			t.Logf("multi-hop: retrieved=%v want=%v recall=%.2f (%d/%d)",
				retrieved, tt.wantKeys, recall, found, len(tt.wantKeys))

			// Hard pass: keyword-reachable pieces should appear
			// Benchmark: full recall means ALL pieces surfaced in one search
			if recall < 0.5 {
				t.Logf("BENCHMARK: multi-hop %s recall=%.2f (less than half the required pieces)", tt.name, recall)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// TEMPORAL RECALL: Recency-sensitive queries
//
// Agent scenario: user asks about "yesterday's deploy" or "what happened
// recently" — the system must prefer recent memories over older ones
// with similar content. Tests the recency factor in scoring.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalTemporalRecall(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	tests := []struct {
		name         string
		query        string
		ns           string
		wantFirst    string   // expected top-1 result
		wantInTopK   []string // expected somewhere in top-K
		notWantFirst []string // should NOT be top-1
		topK         int
	}{
		{
			// "what did we deploy yesterday" — should get deploy-yesterday, not older deploys
			name:         "yesterday_deploy",
			query:        "what did we deploy yesterday",
			wantFirst:    "deploy-yesterday",
			notWantFirst: []string{"deploy-last-week", "deploy-last-month"},
			topK:         5,
		},
		{
			// "most recent deployment" — recency should dominate
			name:         "most_recent_deploy",
			query:        "most recent production deployment",
			wantFirst:    "deploy-yesterday",
			notWantFirst: []string{"deploy-last-month"},
			topK:         5,
		},
		{
			// "what happened last week" — deploy-last-week should be preferred
			name:      "last_week",
			query:     "what happened last week in the project",
			wantFirst: "deploy-last-week",
			topK:      5,
		},
		{
			// All three deploys should be retrievable when asking broadly
			name:       "all_deploys",
			query:      "deployment history production changes",
			wantInTopK: []string{"deploy-yesterday", "deploy-last-week", "deploy-last-month"},
			topK:       10,
		},
		{
			// Recent incident (march) should rank above old one (january)
			name:         "recent_incident_over_old",
			query:        "production outage performance degradation",
			wantFirst:    "perf-incident-mar",
			notWantFirst: []string{"perf-incident-jan"},
			topK:         5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := SearchParams{Query: tt.query, Limit: tt.topK}
			if tt.ns != "" {
				params.NS = tt.ns
			}
			results, err := s.Search(ctx, params)
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			retrieved := extractKeys(results)

			if tt.wantFirst != "" && len(retrieved) > 0 {
				if retrieved[0] != tt.wantFirst {
					t.Logf("BENCHMARK: wanted %q at rank 1, got %q (retrieved=%v)", tt.wantFirst, retrieved[0], retrieved)
				}
			}

			for _, nk := range tt.notWantFirst {
				if len(retrieved) > 0 && retrieved[0] == nk {
					t.Logf("BENCHMARK: %q should not be rank 1 for temporal query %q", nk, tt.query)
				}
			}

			if len(tt.wantInTopK) > 0 {
				retrievedSet := make(map[string]bool, len(retrieved))
				for _, k := range retrieved {
					retrievedSet[k] = true
				}
				found := 0
				for _, k := range tt.wantInTopK {
					if retrievedSet[k] {
						found++
					}
				}
				recall := float64(found) / float64(len(tt.wantInTopK))
				t.Logf("temporal: retrieved=%v want_in_top_k=%v recall=%.2f", retrieved, tt.wantInTopK, recall)
				if recall < 0.5 {
					t.Logf("BENCHMARK: temporal %s recall=%.2f", tt.name, recall)
				}
			} else {
				t.Logf("temporal: retrieved=%v want_first=%s", retrieved, tt.wantFirst)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// NEGATION / ABSENCE: Queries about what is NOT used
//
// Agent scenario: user asks "do we use Redis?" or "is there a GraphQL API?"
// The system should surface the memory that explicitly says "no" rather
// than returning nothing or returning a false-positive match.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalNegation(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		query      string
		wantKeys   []string // memories that answer the question (even if answer is "no")
		notKeys    []string // memories that would be false positives
		topK       int
	}{
		{
			// "do we use Redis?" — should find no-redis, not data-layer (PostgreSQL)
			name:     "redis_absence",
			query:    "do we use Redis for caching",
			wantKeys: []string{"no-redis"},
			topK:     5,
		},
		{
			// "is there a GraphQL API?" — should find no-graphql
			name:     "graphql_absence",
			query:    "do we have a GraphQL API",
			wantKeys: []string{"no-graphql"},
			topK:     5,
		},
		{
			// "what caching layer do we use?" — should find no-redis (explains sync.Map approach)
			name:     "caching_strategy",
			query:    "what caching strategy does the project use",
			wantKeys: []string{"no-redis"},
			topK:     5,
		},
		{
			// "does project beta use a relational database?" — should find beta-storage
			// which says "No relational database used" — NOT alpha's PostgreSQL
			name:     "beta_no_rdb",
			query:    "does project beta use a relational database",
			wantKeys: []string{"beta-storage"},
			notKeys:  []string{"data-layer"}, // alpha's PostgreSQL would be a false positive
			topK:     5,
		},
		{
			// "what API style does alpha use?" — should find no-graphql (REST exclusively)
			// and possibly atlas-routing or auth-flow
			name:     "api_style",
			query:    "what API style does the project use REST or GraphQL",
			wantKeys: []string{"no-graphql"},
			topK:     5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := s.Search(ctx, SearchParams{Query: tt.query, Limit: tt.topK})
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			retrieved := extractKeys(results)
			retrievedSet := make(map[string]bool, len(retrieved))
			for _, k := range retrieved {
				retrievedSet[k] = true
			}

			relevant := make(map[string]bool, len(tt.wantKeys))
			for _, k := range tt.wantKeys {
				relevant[k] = true
			}

			recall := RecallAtK(retrieved, tt.wantKeys, tt.topK)
			mrr := MRR(retrieved, relevant)

			t.Logf("negation: retrieved=%v want=%v recall=%.2f mrr=%.2f",
				retrieved, tt.wantKeys, recall, mrr)

			if recall < 1.0 {
				t.Logf("BENCHMARK: negation/%s recall=%.2f — absence memory not surfaced", tt.name, recall)
			}

			for _, nk := range tt.notKeys {
				if retrievedSet[nk] {
					// In ns-scoped queries this would be a hard fail; here it's a signal
					t.Logf("BENCHMARK: false positive %q in results for %q", nk, tt.query)
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// SCALE: Search quality and latency with large corpus
//
// Seeds 500+ memories across many namespaces, then verifies:
//   - Search still returns relevant results (signal vs noise)
//   - Latency stays reasonable (< 100ms per search)
//   - Context assembly respects budget even with many candidates
// ═══════════════════════════════════════════════════════════════════════

func TestEvalScale(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seed the real corpus first (the "signal")
	ids, err := SeedStore(ctx, s, DefaultSeedCorpus())
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	// Add 500 noise memories across 10 namespaces
	noiseCount := 500
	noiseNS := []string{
		"project:gamma", "project:delta", "project:epsilon",
		"project:zeta", "project:eta", "project:theta",
		"project:iota", "project:kappa", "user:notes", "system:logs",
	}
	noiseTopics := []string{
		"frontend React component rendering performance optimization techniques",
		"Kubernetes pod scheduling affinity and anti-affinity rules configuration",
		"machine learning model hyperparameter tuning with Bayesian optimization",
		"OAuth 2.0 PKCE flow implementation for mobile native applications",
		"WebSocket connection management reconnection backoff strategies",
		"Docker multi-stage build optimization layer caching best practices",
		"Apache Kafka consumer group rebalancing partition assignment strategies",
		"Terraform state management remote backends locking mechanisms",
		"gRPC streaming bidirectional communication protocol buffer definitions",
		"Elasticsearch index lifecycle management rollover warm cold tiers",
	}

	for i := 0; i < noiseCount; i++ {
		topic := noiseTopics[i%len(noiseTopics)]
		ns := noiseNS[i%len(noiseNS)]
		_, err := s.Put(ctx, PutParams{
			NS:         ns,
			Key:        fmt.Sprintf("noise-%d", i),
			Content:    fmt.Sprintf("Noise memory #%d about %s. Variation %d adds unique terms: item-%d batch-%d.", i, topic, i%7, i*3, i*5),
			Kind:       "semantic",
			Priority:   "normal",
			Importance: 0.3 + float64(i%5)*0.1,
		})
		if err != nil {
			t.Fatalf("noise seed %d: %v", i, err)
		}
	}

	// Verify total count
	count, err := s.MemoryCount(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	expectedMin := int64(len(ids) + noiseCount)
	t.Logf("store size: %d memories (seed=%d noise=%d)", count, len(ids), noiseCount)
	if count < expectedMin {
		t.Errorf("expected at least %d memories, got %d", expectedMin, count)
	}

	t.Run("signal_survives_noise", func(t *testing.T) {
		// Core signal queries should still find the right memories
		// even with 500+ noise memories
		cases := []struct {
			query    string
			wantKey  string
		}{
			{"PostgreSQL pgvector connection pooling", "data-layer"},
			{"JWT refresh token authentication", "auth-flow"},
			{"incident response runbook procedure", "runbook"},
			{"deploy production ArgoCD ECS", "ship-process"},
		}

		for _, tc := range cases {
			results, err := s.Search(ctx, SearchParams{Query: tc.query, Limit: 5})
			if err != nil {
				t.Fatalf("search %q: %v", tc.query, err)
			}
			retrieved := extractKeys(results)
			found := false
			for _, k := range retrieved {
				if k == tc.wantKey {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("signal lost: %q not in top-5 for %q (got %v)", tc.wantKey, tc.query, retrieved)
			} else {
				t.Logf("signal OK: %q found for %q", tc.wantKey, tc.query)
			}
		}
	})

	t.Run("noise_stays_in_noise_ns", func(t *testing.T) {
		// NS-scoped search should not return noise from other namespaces
		results, err := s.Search(ctx, SearchParams{
			NS: "project:alpha", Query: "performance optimization", Limit: 10,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, r := range results {
			if strings.HasPrefix(r.Key, "noise-") {
				t.Errorf("noise memory %q leaked into project:alpha search", r.Key)
			}
		}
	})

	t.Run("search_latency", func(t *testing.T) {
		queries := []string{
			"database connection pool exhaustion",
			"how to deploy to production",
			"user authentication JWT tokens",
			"incident response procedures",
			"what tools are available",
		}

		var totalDuration time.Duration
		for _, q := range queries {
			start := time.Now()
			_, err := s.Search(ctx, SearchParams{Query: q, Limit: 10})
			elapsed := time.Since(start)
			totalDuration += elapsed
			if err != nil {
				t.Fatalf("search %q: %v", q, err)
			}
		}

		avgMs := totalDuration.Milliseconds() / int64(len(queries))
		t.Logf("avg search latency: %dms over %d queries (%d total memories)",
			avgMs, len(queries), count)

		if avgMs > 100 {
			t.Errorf("search too slow: avg %dms > 100ms threshold", avgMs)
		}
	})

	t.Run("context_budget_at_scale", func(t *testing.T) {
		// Context assembly with 500+ candidates should still respect budget
		result, err := s.Context(ctx, ContextParams{
			Query:    "database architecture and deployment",
			Budget:   500,
			PinTiers: []string{"identity", "ltm"},
		})
		if err != nil {
			t.Fatalf("context: %v", err)
		}

		tolerance := 20
		if result.Used > result.Budget+tolerance {
			t.Errorf("budget exceeded at scale: %d > %d", result.Used, result.Budget)
		}
		if len(result.Memories) == 0 {
			t.Error("no memories returned at scale")
		}

		t.Logf("context at scale: budget=%d used=%d memories=%d",
			result.Budget, result.Used, len(result.Memories))
	})
}

// ═══════════════════════════════════════════════════════════════════════
// EVAL REPORT: Aggregate runner with JSON output
// ═══════════════════════════════════════════════════════════════════════

func TestEvalReport(t *testing.T) {
	s, ids := seedEvalStore(t)
	ctx := context.Background()

	report := EvalReport{
		Timestamp: time.Now().UTC(),
		EmbedMode: os.Getenv("GHOST_EMBED_PROVIDER"),
	}
	if report.EmbedMode == "" {
		report.EmbedMode = "none"
	}

	// ── Hot path: Layer 1 (SystemPrompt via List) ──
	hotListCases := []struct {
		name       string
		namespaces []string
		charBudget int
		mustContain []string
	}{
		{"hot/list/identity_loaded", []string{"user:prefs"}, 8000,
			[]string{"concise, technical responses", "direct and opinionated"}},
		{"hot/list/tools_loaded", []string{"system:tools"}, 8000,
			[]string{"shell-search", "shell-browser"}},
		{"hot/list/budget_respected", []string{"user:prefs", "system:tools"}, 1200, nil},
	}

	for _, tc := range hotListCases {
		prompt := simulateSystemPrompt(t, s, tc.namespaces, tc.charBudget)
		sc := ScenarioResult{Name: tc.name, Category: "hot_path_list", Metrics: map[string]float64{}, Pass: true}
		sc.Metrics["prompt_length"] = float64(len(prompt))

		for _, substr := range tc.mustContain {
			if !strings.Contains(prompt, substr) {
				sc.Pass = false
				sc.Errors = append(sc.Errors, "missing: "+substr)
			}
		}
		if tc.charBudget < 8000 && len(prompt) > tc.charBudget {
			sc.Pass = false
			sc.Errors = append(sc.Errors, fmt.Sprintf("exceeded budget: %d > %d", len(prompt), tc.charBudget))
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Hot path: Layer 2 (InjectContext via Context) ──
	hotContextCases := []struct {
		name     string
		query    string
		ns       string
		budget   int
		mustHave []string
	}{
		{"hot/context/db_pooling", "help me fix the database connection pooling issue", "", 1000, nil},
		{"hot/context/tight_budget", "tell me about the deployment process", "", 300, nil},
		{"hot/context/ns_scoped", "database issues", "project:alpha", 2000, nil},
	}

	for _, tc := range hotContextCases {
		result, err := s.Context(ctx, ContextParams{
			NS: tc.ns, Query: tc.query, Budget: tc.budget, PinTiers: []string{"identity", "ltm"},
		})
		sc := ScenarioResult{Name: tc.name, Category: "hot_path_context", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}

		sc.Metrics["budget_utilization"] = float64(result.Used) / float64(result.Budget)
		sc.Metrics["memory_count"] = float64(len(result.Memories))
		tolerance := 20
		sc.Pass = result.Used <= result.Budget+tolerance && len(result.Memories) > 0

		for _, key := range tc.mustHave {
			if !contextHasKey(result, key) {
				sc.Pass = false
				sc.Errors = append(sc.Errors, "missing: "+key)
			}
		}
		if tc.ns != "" {
			for _, m := range result.Memories {
				if m.NS != tc.ns && !strings.HasPrefix(m.NS, tc.ns) {
					sc.Pass = false
					sc.Errors = append(sc.Errors, "ns leakage: "+m.NS+"/"+m.Key)
				}
			}
		}
		if !sc.Pass && len(sc.Errors) == 0 {
			sc.Errors = append(sc.Errors, "budget exceeded or empty")
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Cold path (search) ──
	type searchCase struct {
		name, difficulty, query, ns string
		wantKeys, notKeys          []string
		topK                       int
	}
	searchCases := []searchCase{
		{"cold/keyword/postgresql", "keyword", "PostgreSQL pgvector", "", []string{"data-layer"}, nil, 3},
		{"cold/keyword/jwt", "keyword", "JWT refresh token", "", []string{"auth-flow"}, nil, 3},
		{"cold/keyword/ns_isolation", "keyword", "pool exhaustion", "project:alpha", []string{"perf-incident-jan"}, []string{"beta-pool-fix", "beta-pool-incident"}, 5},
		{"cold/semantic/how_to_ship", "semantic", "how do we get code to production", "", []string{"ship-process"}, nil, 5},
		{"cold/semantic/editor_pref", "semantic", "what IDE setup does the user prefer", "", []string{"editor-setup"}, nil, 5},
		{"cold/semantic/error_pattern", "semantic", "how should I handle errors in the codebase", "", []string{"fault-handling"}, nil, 5},
		{"cold/semantic/availability", "semantic", "when can I reach the user", "", []string{"schedule"}, nil, 5},
		{"cold/semantic/tool_lookup", "semantic", "how do I search the web", "", []string{"tool-search"}, nil, 5},
		{"cold/semantic/project_origin", "semantic", "how did this project start", "", []string{"lore-origin"}, nil, 5},
		{"cold/adversarial/pool_disambig", "adversarial", "database connection pool exhaustion", "", []string{"perf-incident-jan"}, nil, 3},
		{"cold/adversarial/recent_incident", "adversarial", "the most recent production incident", "", []string{"perf-incident-mar"}, nil, 3},
	}

	var keywordMRR, semanticMRR, adversarialMRR float64
	var keywordN, semanticN, adversarialN int

	for _, tc := range searchCases {
		params := SearchParams{Query: tc.query, Limit: tc.topK}
		if tc.ns != "" {
			params.NS = tc.ns
		}
		results, err := s.Search(ctx, params)
		sc := ScenarioResult{Name: tc.name, Category: "cold_path", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}

		retrieved := extractKeys(results)
		relevant := make(map[string]bool, len(tc.wantKeys))
		for _, k := range tc.wantKeys {
			relevant[k] = true
		}

		mrr := MRR(retrieved, relevant)
		recall := RecallAtK(retrieved, tc.wantKeys, tc.topK)
		sc.Metrics["precision_at_k"] = PrecisionAtK(retrieved, tc.wantKeys, tc.topK)
		sc.Metrics["recall_at_k"] = recall
		sc.Metrics["mrr"] = mrr

		switch tc.difficulty {
		case "keyword":
			sc.Pass = recall >= 1.0
			keywordMRR += mrr
			keywordN++
		case "semantic":
			sc.Pass = recall >= 1.0
			semanticMRR += mrr
			semanticN++
		case "adversarial":
			sc.Pass = recall >= 1.0
			adversarialMRR += mrr
			adversarialN++
		}

		retrievedSet := make(map[string]bool, len(retrieved))
		for _, k := range retrieved {
			retrievedSet[k] = true
		}
		for _, nk := range tc.notKeys {
			if retrievedSet[nk] {
				sc.Pass = false
				sc.Errors = append(sc.Errors, "unwanted: "+nk)
			}
		}
		if !sc.Pass && len(sc.Errors) == 0 {
			sc.Errors = append(sc.Errors, "recall < 1.0")
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Memory correction ──
	correctedContent := "The sqlc migration is complete as of 2024-03-20. All user service endpoints now use sqlc. GORM has been fully removed. N+1 query issues are resolved."
	s.Put(ctx, PutParams{
		NS: "project:alpha", Key: "legacy-orm", Content: correctedContent,
		Kind: "semantic", Priority: "normal", Importance: 0.6,
	})

	correctionChecks := []struct {
		name  string
		check func() bool
	}{
		{"correction/get_v2", func() bool {
			mems, _ := s.Get(ctx, GetParams{NS: "project:alpha", Key: "legacy-orm"})
			return len(mems) > 0 && mems[0].Version == 2 && mems[0].Content == correctedContent
		}},
		{"correction/search_v2", func() bool {
			results, _ := s.Search(ctx, SearchParams{Query: "sqlc migration GORM", Limit: 5})
			for _, r := range results {
				if r.Key == "legacy-orm" {
					return r.Version == 2
				}
			}
			return false
		}},
		{"correction/history_both", func() bool {
			mems, _ := s.History(ctx, HistoryParams{NS: "project:alpha", Key: "legacy-orm"})
			return len(mems) >= 2
		}},
	}

	for _, rc := range correctionChecks {
		sc := ScenarioResult{Name: rc.name, Category: "correction", Metrics: map[string]float64{}, Pass: rc.check()}
		if sc.Pass {
			sc.Metrics["correct"] = 1
		} else {
			sc.Metrics["correct"] = 0
			sc.Errors = append(sc.Errors, "correction failed")
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Reflect ──
	// Reset counters and last_accessed_at that earlier search/context calls may have bumped,
	// so reflect sees the original seed state for lifecycle assertions.
	for _, key := range []string{"reflect-decay-target", "reflect-promote-target", "reflect-demote-target", "reflect-prune-target", "reflect-identity-safe"} {
		sm := findSeed(key)
		if sm != nil {
			s.db.ExecContext(ctx, `UPDATE memories SET access_count = ?, last_accessed_at = NULL WHERE id = ?`, sm.AccessCount, ids[key])
		}
	}
	var preImportance float64
	s.db.QueryRow(`SELECT importance FROM memories WHERE id = ?`, ids["reflect-decay-target"]).Scan(&preImportance)
	s.Reflect(ctx, ReflectParams{})

	reflectChecks := []struct {
		name  string
		check func() bool
	}{
		{"reflect/decay", func() bool {
			var imp float64
			s.db.QueryRow(`SELECT importance FROM memories WHERE id = ?`, ids["reflect-decay-target"]).Scan(&imp)
			return imp < preImportance
		}},
		{"reflect/promote", func() bool {
			var tier string
			s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, ids["reflect-promote-target"]).Scan(&tier)
			return tier == "ltm"
		}},
		{"reflect/demote", func() bool {
			var tier string
			s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, ids["reflect-demote-target"]).Scan(&tier)
			return tier == "dormant"
		}},
		{"reflect/prune", func() bool {
			var deletedAt *string
			s.db.QueryRow(`SELECT deleted_at FROM memories WHERE id = ?`, ids["reflect-prune-target"]).Scan(&deletedAt)
			return deletedAt != nil
		}},
		{"reflect/pinned_safe", func() bool {
			var tier string
			var pinned int
			var imp float64
			s.db.QueryRow(`SELECT tier, pinned, importance FROM memories WHERE id = ?`, ids["reflect-identity-safe"]).Scan(&tier, &pinned, &imp)
			return tier == "ltm" && pinned == 1 && imp == 1.0
		}},
	}

	correctReflect := 0
	for _, rc := range reflectChecks {
		sc := ScenarioResult{Name: rc.name, Category: "reflect", Metrics: map[string]float64{}, Pass: rc.check()}
		if sc.Pass {
			correctReflect++
			sc.Metrics["correct"] = 1
		} else {
			sc.Metrics["correct"] = 0
			sc.Errors = append(sc.Errors, "rule incorrect")
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Multi-hop ──
	multiHopCases := []struct {
		name     string
		query    string
		wantKeys []string
		topK     int
	}{
		{"multihop/gateway_to_db", "what database does the API gateway route user requests to",
			[]string{"atlas-routing", "user-service-arch", "data-layer"}, 5},
		{"multihop/search_e2e", "how does the search feature work from API to database",
			[]string{"atlas-routing", "search-service-arch", "data-layer"}, 5},
		{"multihop/deploy_what_how", "what was in the latest deployment and how does deploy work",
			[]string{"deploy-yesterday", "ship-process"}, 5},
		{"multihop/incident_safeguards", "what caused search latency incident and what safeguards exist",
			[]string{"perf-incident-mar", "search-service-arch"}, 5},
	}

	var multiHopRecallSum float64
	for _, tc := range multiHopCases {
		results, err := s.Search(ctx, SearchParams{Query: tc.query, Limit: tc.topK})
		sc := ScenarioResult{Name: tc.name, Category: "multi_hop", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}
		retrievedSet := make(map[string]bool)
		for _, r := range results {
			retrievedSet[r.Key] = true
		}
		found := 0
		for _, k := range tc.wantKeys {
			if retrievedSet[k] {
				found++
			}
		}
		recall := float64(found) / float64(len(tc.wantKeys))
		sc.Metrics["recall"] = recall
		sc.Metrics["pieces_found"] = float64(found)
		sc.Metrics["pieces_needed"] = float64(len(tc.wantKeys))
		sc.Pass = recall >= 0.5 // at least half the required pieces
		if !sc.Pass {
			sc.Errors = append(sc.Errors, fmt.Sprintf("recall %.2f < 0.5", recall))
		}
		multiHopRecallSum += recall
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Temporal ──
	temporalCases := []struct {
		name      string
		query     string
		wantFirst string
		topK      int
	}{
		{"temporal/yesterday_deploy", "what did we deploy yesterday", "deploy-yesterday", 5},
		{"temporal/most_recent_deploy", "most recent production deployment", "deploy-yesterday", 5},
		{"temporal/recent_incident", "production outage performance degradation", "perf-incident-mar", 5},
	}

	temporalCorrect := 0
	for _, tc := range temporalCases {
		results, err := s.Search(ctx, SearchParams{Query: tc.query, Limit: tc.topK})
		sc := ScenarioResult{Name: tc.name, Category: "temporal", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}
		retrieved := extractKeys(results)
		relevant := map[string]bool{tc.wantFirst: true}
		sc.Metrics["mrr"] = MRR(retrieved, relevant)
		sc.Pass = len(retrieved) > 0 && retrieved[0] == tc.wantFirst
		if sc.Pass {
			temporalCorrect++
		} else if len(retrieved) > 0 {
			sc.Errors = append(sc.Errors, fmt.Sprintf("got %q at rank 1, want %q", retrieved[0], tc.wantFirst))
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Negation ──
	negationCases := []struct {
		name     string
		query    string
		wantKeys []string
		notKeys  []string
		topK     int
	}{
		{"negation/redis_absence", "do we use Redis for caching", []string{"no-redis"}, nil, 5},
		{"negation/graphql_absence", "do we have a GraphQL API", []string{"no-graphql"}, nil, 5},
		{"negation/beta_no_rdb", "does project beta use a relational database", []string{"beta-storage"}, []string{"data-layer"}, 5},
	}

	negationCorrect := 0
	for _, tc := range negationCases {
		results, err := s.Search(ctx, SearchParams{Query: tc.query, Limit: tc.topK})
		sc := ScenarioResult{Name: tc.name, Category: "negation", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}
		retrieved := extractKeys(results)
		relevant := make(map[string]bool, len(tc.wantKeys))
		for _, k := range tc.wantKeys {
			relevant[k] = true
		}
		sc.Metrics["recall"] = RecallAtK(retrieved, tc.wantKeys, tc.topK)
		sc.Metrics["mrr"] = MRR(retrieved, relevant)
		sc.Pass = sc.Metrics["recall"] >= 1.0

		retrievedSet := make(map[string]bool, len(retrieved))
		for _, k := range retrieved {
			retrievedSet[k] = true
		}
		for _, nk := range tc.notKeys {
			if retrievedSet[nk] {
				sc.Errors = append(sc.Errors, "false_positive: "+nk)
			}
		}
		if sc.Pass {
			negationCorrect++
		} else {
			sc.Errors = append(sc.Errors, "absence memory not found")
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── No-result precision ──
	noResultCases := []struct {
		name          string
		query         string
		dangerousKeys []string
	}{
		{"no_result/kubernetes", "Kubernetes pod deployment configuration", []string{"ship-process"}},
		{"no_result/mongodb", "MongoDB replica set configuration", []string{"data-layer"}},
		{"no_result/cooking", "best recipe for chocolate soufflé", nil},
	}

	for _, tc := range noResultCases {
		results, err := s.Search(ctx, SearchParams{Query: tc.query, Limit: 5})
		sc := ScenarioResult{Name: tc.name, Category: "no_result", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}
		sc.Metrics["result_count"] = float64(len(results))
		dangerousHits := 0
		for _, dk := range tc.dangerousKeys {
			for i, r := range results {
				if r.Key == dk && i < 3 {
					dangerousHits++
					sc.Errors = append(sc.Errors, fmt.Sprintf("dangerous: %s at rank %d", dk, i+1))
				}
			}
		}
		sc.Metrics["dangerous_hits"] = float64(dangerousHits)
		sc.Pass = dangerousHits == 0
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Vague queries ──
	vagueCases := []struct {
		name       string
		query      string
		acceptable []string
	}{
		{"vague/the_config", "the config", []string{"alerts-config", "editor-setup"}},
		{"vague/testing", "how do we test things", []string{"test-pyramid"}},
		{"vague/deploy", "deploy", []string{"ship-process", "deploy-yesterday", "deploy-last-week", "deploy-last-month"}},
	}

	vagueHits := 0
	for _, tc := range vagueCases {
		results, err := s.Search(ctx, SearchParams{Query: tc.query, Limit: 5})
		sc := ScenarioResult{Name: tc.name, Category: "vague", Metrics: map[string]float64{}}
		if err != nil {
			sc.Errors = append(sc.Errors, err.Error())
			report.Scenarios = append(report.Scenarios, sc)
			continue
		}
		acceptSet := make(map[string]bool, len(tc.acceptable))
		for _, k := range tc.acceptable {
			acceptSet[k] = true
		}
		hits := 0
		for _, r := range results {
			if acceptSet[r.Key] {
				hits++
			}
		}
		sc.Metrics["acceptable_hits"] = float64(hits)
		sc.Metrics["total_results"] = float64(len(results))
		sc.Pass = hits > 0
		if sc.Pass {
			vagueHits++
		} else {
			sc.Errors = append(sc.Errors, "no acceptable results")
		}
		report.Scenarios = append(report.Scenarios, sc)
	}

	// ── Context efficiency ──
	effResult, err := s.Context(ctx, ContextParams{
		Query: "pgvector index corruption vacuum", Budget: 200, PinTiers: []string{"identity"},
	})
	effSc := ScenarioResult{Name: "efficiency/relevant_not_crowded", Category: "efficiency", Metrics: map[string]float64{}}
	if err == nil {
		effSc.Metrics["memory_count"] = float64(len(effResult.Memories))
		effSc.Metrics["budget_util"] = float64(effResult.Used) / float64(effResult.Budget)
		effSc.Pass = contextHasKey(effResult, "perf-incident-mar")
		if !effSc.Pass {
			effSc.Errors = append(effSc.Errors, "relevant memory crowded out by pins")
		}
	} else {
		effSc.Errors = append(effSc.Errors, err.Error())
	}
	report.Scenarios = append(report.Scenarios, effSc)

	// ── Summary ──
	for _, sc := range report.Scenarios {
		report.Summary.Total++
		if sc.Pass {
			report.Summary.Passed++
		} else {
			report.Summary.Failed++
		}
	}

	totalSearchN := keywordN + semanticN + adversarialN
	if totalSearchN > 0 {
		report.Summary.MeanMRR = (keywordMRR + semanticMRR + adversarialMRR) / float64(totalSearchN)
	}
	for _, sc := range report.Scenarios {
		if sc.Category == "cold_path" {
			report.Summary.MeanRecall += sc.Metrics["recall_at_k"]
		}
	}
	if totalSearchN > 0 {
		report.Summary.MeanRecall /= float64(totalSearchN)
	}
	if len(reflectChecks) > 0 {
		report.Summary.ReflectAccuracy = float64(correctReflect) / float64(len(reflectChecks))
	}

	breakdown := ScenarioResult{
		Name: "summary/mrr_by_difficulty", Category: "meta",
		Metrics: map[string]float64{}, Pass: true,
	}
	if keywordN > 0 {
		breakdown.Metrics["keyword_mrr"] = keywordMRR / float64(keywordN)
	}
	if semanticN > 0 {
		breakdown.Metrics["semantic_mrr"] = semanticMRR / float64(semanticN)
	}
	if adversarialN > 0 {
		breakdown.Metrics["adversarial_mrr"] = adversarialMRR / float64(adversarialN)
	}
	if len(multiHopCases) > 0 {
		breakdown.Metrics["multi_hop_recall"] = multiHopRecallSum / float64(len(multiHopCases))
	}
	if len(temporalCases) > 0 {
		breakdown.Metrics["temporal_accuracy"] = float64(temporalCorrect) / float64(len(temporalCases))
	}
	if len(negationCases) > 0 {
		breakdown.Metrics["negation_accuracy"] = float64(negationCorrect) / float64(len(negationCases))
	}
	if len(vagueCases) > 0 {
		breakdown.Metrics["vague_accuracy"] = float64(vagueHits) / float64(len(vagueCases))
	}
	report.Scenarios = append(report.Scenarios, breakdown)
	report.Summary.Total++
	report.Summary.Passed++

	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("EVAL_REPORT:%s", string(reportJSON))

	// Hard-fail: hot path, keyword search, correction, reflect
	hardFails := 0
	for _, sc := range report.Scenarios {
		if sc.Pass || sc.Category == "meta" {
			continue
		}
		switch sc.Category {
		case "hot_path_list", "hot_path_context", "reflect", "correction":
			hardFails++
		case "cold_path":
			for _, tc := range searchCases {
				if tc.name == sc.Name && tc.difficulty == "keyword" {
					hardFails++
				}
			}
		}
	}
	if hardFails > 0 {
		t.Errorf("%d hard failures", hardFails)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// DORMANT SUPPRESSION: Archived memories should not surface
//
// Agent scenario: user archives a memory via curate(op="archive").
// That memory should not appear in search or context results by default.
// ═══════════════════════════════════════════════════════════════════════

func TestEvalDormantSuppression(t *testing.T) {
	s, _ := seedEvalStore(t)
	ctx := context.Background()

	t.Run("dormant_excluded_from_search", func(t *testing.T) {
		// "deployment process" should match ship-process but NOT dormant-old-deploy-process
		results, err := s.Search(ctx, SearchParams{Query: "deployment process production", Limit: 10})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, r := range results {
			if r.Key == "dormant-old-deploy-process" {
				t.Errorf("dormant memory appeared in search results: %s (tier=%s)", r.Key, r.Tier)
			}
		}
		// ship-process should still be found
		found := false
		for _, r := range results {
			if r.Key == "ship-process" {
				found = true
			}
		}
		if !found {
			t.Logf("BENCHMARK: ship-process not in top-10 for 'deployment process production'")
		}
	})

	t.Run("dormant_excluded_from_context", func(t *testing.T) {
		result, err := s.Context(ctx, ContextParams{Query: "how to deploy code", Budget: 2000})
		if err != nil {
			t.Fatalf("context: %v", err)
		}
		if contextHasKey(result, "dormant-old-deploy-process") {
			t.Errorf("dormant memory appeared in context results")
		}
	})

	t.Run("dormant_retrievable_with_include_all", func(t *testing.T) {
		results, err := s.Search(ctx, SearchParams{
			Query:      "deployment process production",
			Limit:      10,
			IncludeAll: true,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		found := false
		for _, r := range results {
			if r.Key == "dormant-old-deploy-process" {
				found = true
			}
		}
		if !found {
			t.Logf("BENCHMARK: dormant memory not found even with IncludeAll")
		}
	})
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

func findSeed(key string) *SeedMemory {
	for _, sm := range DefaultSeedCorpus() {
		if sm.Key == key {
			return &sm
		}
	}
	return nil
}
