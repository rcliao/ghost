package store

import (
	"context"
	"fmt"
	"time"
)

// SeedMemory describes a memory to be seeded for eval benchmarks.
type SeedMemory struct {
	NS, Key, Content, Kind, Tier, Priority string
	Pinned        bool    // always loaded in context, exempt from decay
	Importance    float64
	BackdateHours int // set created_at to N hours ago via raw SQL
	AccessCount   int // override access_count via raw SQL
	UtilityCount  int // override utility_count via raw SQL
}

// DefaultSeedCorpus returns ~35 memories modeling a realistic agent memory store.
//
// Three agent use cases drive the design:
//
//  1. HOT PATH (system prompt assembly): identity, soul, lore, tools, user prefs —
//     must be assembled under token budgets with correct prioritization.
//  2. COLD PATH (conversation recall): project facts, incidents, procedures —
//     recalled mid-conversation via natural language queries.
//  3. MEMORY CORRECTION: outdated facts that get updated via Put() versioning —
//     old versions must not leak into search/context.
func DefaultSeedCorpus() []SeedMemory {
	return []SeedMemory{
		// ══════════════════════════════════════════════════════════════
		// IDENTITY / SOUL — who the agent is (hot path: always loaded, pinned)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "user:prefs", Key: "identity-core", Kind: "semantic", Tier: "ltm", Pinned: true, Priority: "critical", Importance: 1.0,
			Content: "I assist with Go and TypeScript codebases. I give concise, technical responses. I cite specific files and line numbers when making recommendations.",
		},
		{
			NS: "user:prefs", Key: "personality", Kind: "semantic", Tier: "ltm", Pinned: true, Priority: "critical", Importance: 1.0,
			Content: "I am direct and opinionated about code quality. I push back on over-engineering. I prefer working solutions over perfect abstractions.",
		},
		{
			NS: "user:prefs", Key: "boundaries", Kind: "semantic", Tier: "ltm", Pinned: true, Priority: "critical", Importance: 0.98,
			Content: "I never modify files outside the current project without asking. I always run tests after code changes. I do not commit unless explicitly asked.",
		},

		// ══════════════════════════════════════════════════════════════
		// USER PREFERENCES — how to interact (hot path: high priority)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "user:prefs", Key: "editor-setup", Kind: "semantic", Tier: "ltm", Pinned: true, Priority: "high", Importance: 0.95,
			Content: "User prefers VS Code with vim keybindings. Go files formatted with gofmt, TypeScript with prettier. Tab width 4 for Go, 2 for TS.",
		},
		{
			NS: "user:prefs", Key: "comm-style", Kind: "semantic", Tier: "ltm", Priority: "high", Importance: 0.85,
			Content: "User wants direct, no-nonsense answers. Lead with the solution, explain after. Use code snippets over prose. No pleasantries or emoji.",
		},
		{
			NS: "user:prefs", Key: "schedule", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.6,
			Content: "User is in Pacific timezone. Available 10am-6pm PT on weekdays. Prefers async communication over meetings.",
		},
		{
			NS: "user:prefs", Key: "git-prefs", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.7,
			Content: "User's git: rcliao01@gmail.com. Conventional commits format. Never amend published commits. Always create new commits rather than amending.",
		},

		// ══════════════════════════════════════════════════════════════
		// LORE — project history and team culture (hot path: contextual)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "project:alpha", Key: "lore-origin", Kind: "episodic", Tier: "ltm", Priority: "normal", Importance: 0.6,
			Content: "Project Alpha started as a hackathon prototype in 2023. Originally a monolith, rewritten to microservices in Q2 2024. The team calls the legacy code 'the monolith'.",
		},
		{
			NS: "project:alpha", Key: "lore-naming", Kind: "semantic", Tier: "ltm", Priority: "low", Importance: 0.4,
			Content: "Internal naming: 'Atlas' is the API gateway, 'Forge' is the build system, 'Vault' is the secrets manager. The team uses Greek mythology names.",
		},

		// ══════════════════════════════════════════════════════════════
		// TOOLS — what the agent has access to (hot path: capability awareness)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "system:tools", Key: "tool-search", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.7,
			Content: "Available tool: shell-search for web queries via Brave/Tavily/DuckDuckGo APIs. Use for current information, documentation lookups, and fact-checking.",
		},
		{
			NS: "system:tools", Key: "tool-browser", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.7,
			Content: "Available tool: shell-browser for headless Chrome automation. Use for scraping web pages, taking screenshots, and interacting with web UIs.",
		},
		{
			NS: "system:tools", Key: "tool-imagen", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.65,
			Content: "Available tool: shell-imagen for image generation via Google Gemini API. Can create images from text descriptions.",
		},

		// ══════════════════════════════════════════════════════════════
		// CURRENT WORK — ephemeral state (hot path: recency matters)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "user:prefs", Key: "current-work", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.5,
			Content: "This week: implementing vector similarity search for the memory system and fixing token budget overflow in context assembly.",
		},

		// ══════════════════════════════════════════════════════════════
		// project:alpha — technical facts (cold path: recalled on demand)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "project:alpha", Key: "data-layer", Kind: "semantic", Tier: "ltm", Priority: "high", Importance: 0.9,
			Content: "The system uses PostgreSQL 15 with pgvector extension for similarity search. Connection pooling through PgBouncer keeps p99 latency under 5ms. Read replicas serve analytics workloads.",
		},
		{
			NS: "project:alpha", Key: "ship-process", Kind: "procedural", Tier: "ltm", Priority: "normal", Importance: 0.7,
			Content: "Releasing code: merge PR to main, CI builds container image, pushes to ECR, ArgoCD syncs to ECS cluster. Blue-green swap with automatic rollback on failed health checks.",
		},
		{
			NS: "project:alpha", Key: "auth-flow", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.65,
			Content: "REST API authentication: short-lived JWT access tokens (15min) with opaque refresh tokens stored server-side. Rate limiting at 100 req/min per API key via token bucket.",
		},
		{
			NS: "project:alpha", Key: "fault-handling", Kind: "procedural", Tier: "ltm", Priority: "high", Importance: 0.8,
			Content: "All domain errors implement AppError interface (Code, Message, Unwrap). Use Result[T] for expected failures, panic only in truly unrecoverable init paths. Wrap external errors at service boundaries.",
		},
		{
			NS: "project:alpha", Key: "test-pyramid", Kind: "procedural", Tier: "stm", Priority: "normal", Importance: 0.5,
			Content: "Unit tests for pure business logic (fast, isolated). Integration tests hit real Postgres via testcontainers. E2E tests cover critical purchase and signup flows. Target: 80% line coverage.",
		},
		{
			NS: "project:alpha", Key: "observability", Kind: "semantic", Tier: "stm", Priority: "low", Importance: 0.3,
			Content: "Prometheus scrapes /metrics every 15s. Grafana dashboards for SLOs. PagerDuty alerts on p99 > 500ms or 5xx rate > 1%. Structured JSON logs via zerolog.",
		},
		{
			NS: "project:alpha", Key: "go-version", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.5,
			Content: "Go 1.22, stdlib net/http router.",
		},

		// ── Incidents (cold path: temporal queries) ──────────────────
		{
			NS: "project:alpha", Key: "perf-incident-jan", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.55,
			BackdateHours: 720,
			Content: "2024-01-10 outage: connection pool exhaustion under load spike. Root cause was missing statement_timeout on long-running analytics queries bleeding into the primary pool.",
		},
		{
			NS: "project:alpha", Key: "perf-incident-mar", Kind: "episodic", Tier: "stm", Priority: "high", Importance: 0.75,
			Content: "2024-03-22 incident: search latency spike to 12s. Caused by pgvector index corruption after vacuum. Fixed by REINDEX CONCURRENTLY. Added monitoring for index bloat.",
		},

		// ── Memory correction target ────────────────────────────────
		{
			// This will be updated via Put() in the correction test.
			// V1: says "ORM migration planned for Q3, 2 weeks"
			NS: "project:alpha", Key: "legacy-orm", Kind: "semantic", Tier: "stm", Priority: "normal", Importance: 0.4,
			Content: "The user service still relies on GORM. We plan to migrate to sqlc in Q3 — estimated 2 weeks. The ORM causes N+1 query issues in the listing endpoints.",
		},

		// ══════════════════════════════════════════════════════════════
		// project:beta — distractor namespace (cold path: isolation tests)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "project:beta", Key: "beta-storage", Kind: "semantic", Tier: "ltm", Priority: "high", Importance: 0.85,
			Content: "Training data stored in S3 with Delta Lake format. Feature store backed by DynamoDB. Model artifacts versioned in MLflow tracking server. No relational database used.",
		},
		{
			NS: "project:beta", Key: "beta-pipeline", Kind: "procedural", Tier: "stm", Priority: "normal", Importance: 0.5,
			Content: "ML pipeline: Airflow DAG triggers nightly, pulls features from DynamoDB, trains XGBoost model, evaluates on holdout set, promotes to SageMaker endpoint if AUC > 0.85.",
		},
		{
			NS: "project:beta", Key: "beta-pool-fix", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.45,
			Content: "Fixed thread pool exhaustion in the feature extraction workers. Increased max_workers from 4 to 16 and added backpressure via bounded queue.",
		},
		{
			NS: "project:beta", Key: "beta-pool-incident", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.4,
			BackdateHours: 360,
			Content: "Connection pool exhaustion in the inference service caused cascading timeouts. Similar symptoms to database pool issues but this was HTTP client pools not database.",
		},

		// ══════════════════════════════════════════════════════════════
		// system:ops — operational + reflect trigger candidates
		// ══════════════════════════════════════════════════════════════
		{
			NS: "system:ops", Key: "runbook", Kind: "procedural", Tier: "ltm", Priority: "high", Importance: 0.8,
			Content: "Incident response: 1) Check service health dashboard 2) Review recent changes in deploy log 3) Inspect error rates by endpoint 4) Page on-call if customer-facing.",
		},
		{
			NS: "system:ops", Key: "oncall-rota", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.5,
			Content: "Current rotation: Alice handles Mon-Wed, Bob covers Thu-Fri, Carol is weekend on-call. Escalation to eng manager after 30 minutes.",
		},

		// ── Reflect trigger candidates ──────────────────────────────
		{
			NS: "system:ops", Key: "reflect-decay-target", Kind: "semantic", Tier: "stm", Priority: "normal", Importance: 0.5,
			BackdateHours: 96, AccessCount: 1,
			Content: "Ephemeral operational note about a one-time config change that is no longer relevant.",
		},
		{
			NS: "system:ops", Key: "reflect-promote-target", Kind: "semantic", Tier: "stm", Priority: "normal", Importance: 0.7,
			BackdateHours: 96, AccessCount: 55,
			Content: "Frequently referenced procedure for rotating API keys across all environments.",
		},
		{
			NS: "system:ops", Key: "reflect-demote-target", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.6,
			BackdateHours: 200, AccessCount: 1,
			Content: "Old LTM knowledge about a deprecated monitoring stack that nobody queries anymore.",
		},
		{
			NS: "system:ops", Key: "reflect-prune-target", Kind: "semantic", Tier: "stm", Priority: "low", Importance: 0.3,
			AccessCount: 25, UtilityCount: 1,
			Content: "Memory that gets surfaced often but is almost never marked useful by the agent.",
		},
		{
			NS: "system:ops", Key: "reflect-identity-safe", Kind: "semantic", Tier: "ltm", Pinned: true, Priority: "critical", Importance: 1.0,
			BackdateHours: 200, AccessCount: 0,
			Content: "Core pinned memory that must survive all lifecycle rules unconditionally.",
		},
		{
			NS: "system:ops", Key: "alerts-config", Kind: "semantic", Tier: "stm", Priority: "normal", Importance: 0.45,
			Content: "Alert thresholds: CPU > 80%, memory > 90%, disk > 85%. PagerDuty integration active for production environment only.",
		},

		// ══════════════════════════════════════════════════════════════
		// DORMANT — archived memories that should not surface in search/context
		// ══════════════════════════════════════════════════════════════
		{
			NS: "project:alpha", Key: "dormant-old-deploy-process", Kind: "procedural", Tier: "dormant", Priority: "normal", Importance: 0.5,
			BackdateHours: 480,
			Content: "Old deployment process: manually SSH into production server, pull latest code from git, run database migrations, restart the application service. Replaced by ArgoCD pipeline.",
		},

		// ══════════════════════════════════════════════════════════════
		// SENSORY — ultra-short-lived buffer entries
		// ══════════════════════════════════════════════════════════════
		{
			// Recent sensory input — should be promoted to STM if accessed
			NS: "system:ops", Key: "sensory-attended", Kind: "episodic", Tier: "sensory", Priority: "normal", Importance: 0.3,
			BackdateHours: 2, AccessCount: 3,
			Content: "User asked about database backup strategy and I explained the WAL archiving approach with point-in-time recovery.",
		},
		{
			// Old sensory input — should be deleted (unattended after 4h)
			NS: "system:ops", Key: "sensory-unattended", Kind: "episodic", Tier: "sensory", Priority: "normal", Importance: 0.2,
			BackdateHours: 5, AccessCount: 0,
			Content: "Transient observation about a temporary log message that appeared during testing.",
		},

		// ══════════════════════════════════════════════════════════════
		// MULTI-HOP — facts that must be combined to answer a question
		// ══════════════════════════════════════════════════════════════
		{
			// Connects: API gateway name ("Atlas") → what it routes to
			NS: "project:alpha", Key: "atlas-routing", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.65,
			Content: "Atlas (the API gateway) routes /api/v2/users to the user service and /api/v2/search to the search service. Rate limiting is applied at the gateway layer.",
		},
		{
			// Connects: user service → what ORM/DB it uses (links to data-layer and legacy-orm)
			NS: "project:alpha", Key: "user-service-arch", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.7,
			Content: "The user service is written in Go 1.22. It connects to the primary PostgreSQL instance via PgBouncer. Handles registration, profile updates, and session management.",
		},
		{
			// Connects: search service → pgvector (links to data-layer)
			NS: "project:alpha", Key: "search-service-arch", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.7,
			Content: "The search service uses pgvector for semantic similarity queries. It maintains its own read replica connection. Index rebuilds happen during off-peak hours via cron.",
		},

		// ══════════════════════════════════════════════════════════════
		// TEMPORAL — events at specific times for recency-based retrieval
		// ══════════════════════════════════════════════════════════════
		{
			NS: "project:alpha", Key: "deploy-yesterday", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.6,
			BackdateHours: 18,
			Content: "Deployed v2.4.1 to production yesterday. Changes: added rate limit headers to Atlas responses, bumped Go to 1.22.1, fixed flaky e2e test in signup flow.",
		},
		{
			NS: "project:alpha", Key: "deploy-last-week", Kind: "episodic", Tier: "stm", Priority: "normal", Importance: 0.55,
			BackdateHours: 168,
			Content: "Deployed v2.4.0 to production last week. Changes: migrated auth tokens from symmetric to asymmetric signing (RS256), added PgBouncer health check endpoint.",
		},
		{
			NS: "project:alpha", Key: "deploy-last-month", Kind: "episodic", Tier: "stm", Priority: "low", Importance: 0.4,
			BackdateHours: 720,
			Content: "Deployed v2.3.0 to production last month. Changes: initial pgvector integration, search service extracted from monolith, new Grafana dashboards.",
		},

		// ══════════════════════════════════════════════════════════════
		// NEGATION — facts about what is NOT used (absence queries)
		// ══════════════════════════════════════════════════════════════
		{
			NS: "project:alpha", Key: "no-redis", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.5,
			Content: "Alpha does not use Redis or any external cache. All caching is done in-process with sync.Map for hot config values. We evaluated Redis in Q1 but decided the latency was already acceptable.",
		},
		{
			NS: "project:alpha", Key: "no-graphql", Kind: "semantic", Tier: "ltm", Priority: "normal", Importance: 0.45,
			Content: "Alpha uses REST exclusively. We considered GraphQL but decided against it due to team familiarity and the overhead of schema management. All endpoints follow the /api/v2/{resource} convention.",
		},
	}
}

// SeedStore populates a store with the given corpus and applies raw SQL overrides
// for backdating and counter manipulation. Returns a map of key → memory ID.
func SeedStore(ctx context.Context, s *SQLiteStore, corpus []SeedMemory) (map[string]string, error) {
	ids := make(map[string]string, len(corpus))

	for _, sm := range corpus {
		mem, err := s.Put(ctx, PutParams{
			NS:         sm.NS,
			Key:        sm.Key,
			Content:    sm.Content,
			Kind:       sm.Kind,
			Tier:       sm.Tier,
			Pinned:     sm.Pinned,
			Priority:   sm.Priority,
			Importance: sm.Importance,
		})
		if err != nil {
			return nil, fmt.Errorf("seed %s/%s: %w", sm.NS, sm.Key, err)
		}
		ids[sm.Key] = mem.ID

		// Put now writes tier correctly, but we keep this override for
		// cases where seed setup needs to bypass normal defaults.
		if sm.Tier != "" && sm.Tier != "stm" {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE memories SET tier = ? WHERE id = ?`, sm.Tier, mem.ID); err != nil {
				return nil, fmt.Errorf("set tier %s: %w", sm.Key, err)
			}
		}

		// Apply raw SQL overrides for reflect trigger setup
		if sm.BackdateHours > 0 {
			backdated := time.Now().Add(-time.Duration(sm.BackdateHours) * time.Hour).UTC().Format(time.RFC3339)
			if _, err := s.db.ExecContext(ctx,
				`UPDATE memories SET created_at = ? WHERE id = ?`, backdated, mem.ID); err != nil {
				return nil, fmt.Errorf("backdate %s: %w", sm.Key, err)
			}
		}
		if sm.AccessCount > 0 {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE memories SET access_count = ? WHERE id = ?`, sm.AccessCount, mem.ID); err != nil {
				return nil, fmt.Errorf("set access_count %s: %w", sm.Key, err)
			}
		}
		if sm.UtilityCount > 0 {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE memories SET utility_count = ? WHERE id = ?`, sm.UtilityCount, mem.ID); err != nil {
				return nil, fmt.Errorf("set utility_count %s: %w", sm.Key, err)
			}
		}
	}

	return ids, nil
}
