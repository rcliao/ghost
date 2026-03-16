package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

const evalNS = "eval:ghost"

// evalTestCase defines a query with expected memory keys that should be retrieved.
type evalTestCase struct {
	Query       string   `json:"query"`
	ExpectedKeys []string `json:"expected_keys"`
	Tags        []string `json:"tags,omitempty"`
}

// evalResult holds the result of a single test case.
type evalResult struct {
	Query        string   `json:"query"`
	ExpectedKeys []string `json:"expected_keys"`
	ReturnedKeys []string `json:"returned_keys"`
	Hits         []string `json:"hits"`
	Misses       []string `json:"misses"`
	Precision    float64  `json:"precision"`
	Recall       float64  `json:"recall"`
}

// evalReport holds the overall eval results.
type evalReport struct {
	Timestamp     string       `json:"timestamp"`
	NS            string       `json:"ns"`
	TotalCases    int          `json:"total_cases"`
	AvgPrecision  float64      `json:"avg_precision"`
	AvgRecall     float64      `json:"avg_recall"`
	Results       []evalResult `json:"results"`
	MemoryCount   int64        `json:"memory_count"`
	Reflected     bool         `json:"reflected"`
}

// evalSeedMemories are the test memories seeded into the eval namespace.
// ~80 memories across 10+ topics to create realistic scoring pressure.
var evalSeedMemories = []store.PutParams{
	// === Auth cluster (4) ===
	{NS: evalNS, Key: "auth-jwt-signing", Content: "Authentication uses JWT tokens with RSA256 signing for API access", Kind: "semantic", Tags: []string{"auth", "project:api"}, Importance: 0.7},
	{NS: evalNS, Key: "auth-token-expiry", Content: "JWT access tokens expire after 15 minutes, refresh tokens after 7 days", Kind: "semantic", Tags: []string{"auth", "project:api"}, Importance: 0.6},
	{NS: evalNS, Key: "auth-cookie-storage", Content: "Refresh tokens are stored in httpOnly secure cookies, not localStorage", Kind: "semantic", Tags: []string{"auth", "security"}, Importance: 0.8},
	{NS: evalNS, Key: "auth-session-bug", Content: "Session tokens were stored in plaintext cookies causing security audit failure", Kind: "episodic", Tags: []string{"auth", "debugging"}, Importance: 0.7},

	// === Database cluster (5) ===
	{NS: evalNS, Key: "db-postgres-choice", Content: "Chose PostgreSQL over MySQL for JSONB support and better concurrent write performance", Kind: "semantic", Tags: []string{"database", "project:api"}, Importance: 0.8},
	{NS: evalNS, Key: "db-migration-gotcha", Content: "Always run migrations in a transaction, we lost data once when a migration failed halfway", Kind: "episodic", Tags: []string{"database", "debugging"}, Importance: 0.9},
	{NS: evalNS, Key: "db-indexing-strategy", Content: "Use GIN indexes for JSONB columns and B-tree for UUID primary keys", Kind: "procedural", Tags: []string{"database", "project:api"}, Importance: 0.6},
	{NS: evalNS, Key: "db-connection-pooling", Content: "PostgreSQL connection pool uses pgBouncer in transaction mode, max 200 connections per pod", Kind: "procedural", Tags: []string{"database", "project:api"}, Importance: 0.5},
	{NS: evalNS, Key: "db-vacuum-schedule", Content: "VACUUM ANALYZE runs nightly at 3am UTC via cron job, takes about 20 minutes on production", Kind: "procedural", Tags: []string{"database", "ops"}, Importance: 0.4},

	// === Deployment cluster (4) ===
	{NS: evalNS, Key: "deploy-k8s-rollout", Content: "Use rolling deployment strategy with maxSurge=1 and maxUnavailable=0 for zero downtime", Kind: "procedural", Tags: []string{"deployment", "project:api"}, Importance: 0.7},
	{NS: evalNS, Key: "deploy-health-check", Content: "Health check endpoint must return 200 within 5 seconds or pod gets killed by liveness probe", Kind: "procedural", Tags: []string{"deployment", "debugging"}, Importance: 0.8},
	{NS: evalNS, Key: "deploy-helm-values", Content: "Helm values override for staging uses values-staging.yaml, production uses values-prod.yaml with sealed secrets", Kind: "procedural", Tags: []string{"deployment", "project:api"}, Importance: 0.5},
	{NS: evalNS, Key: "deploy-canary-rollback", Content: "Canary deployments auto-rollback if error rate exceeds 5% over 2 minutes, monitored by Prometheus", Kind: "procedural", Tags: []string{"deployment", "monitoring"}, Importance: 0.7},

	// === Monitoring cluster (5) ===
	{NS: evalNS, Key: "monitor-prometheus-setup", Content: "Prometheus scrapes /metrics endpoint every 15 seconds, retention 30 days, uses Thanos for long-term storage", Kind: "semantic", Tags: []string{"monitoring", "ops"}, Importance: 0.6},
	{NS: evalNS, Key: "monitor-grafana-dashboard", Content: "Main API dashboard at grafana.internal/d/api-latency shows p50/p99 latency, error rate, and throughput", Kind: "procedural", Tags: []string{"monitoring", "ops"}, Importance: 0.5},
	{NS: evalNS, Key: "monitor-alert-rules", Content: "PagerDuty alerts fire when p99 latency exceeds 2 seconds for 5 minutes or error rate exceeds 1% for 3 minutes", Kind: "procedural", Tags: []string{"monitoring", "ops"}, Importance: 0.7},
	{NS: evalNS, Key: "monitor-log-aggregation", Content: "Logs shipped to Loki via Promtail, queryable in Grafana. Structured JSON logging with request_id correlation", Kind: "semantic", Tags: []string{"monitoring", "logging"}, Importance: 0.5},
	{NS: evalNS, Key: "monitor-oncall-runbook", Content: "On-call runbook: check Grafana dashboard first, then Loki logs filtered by request_id, escalate to Slack #incidents", Kind: "procedural", Tags: []string{"monitoring", "ops"}, Importance: 0.6},

	// === CI/CD cluster (4) ===
	{NS: evalNS, Key: "ci-github-actions", Content: "CI runs on GitHub Actions with matrix builds for Go 1.21 and 1.22, parallel test shards", Kind: "semantic", Tags: []string{"ci", "project:api"}, Importance: 0.5},
	{NS: evalNS, Key: "ci-test-coverage", Content: "Test coverage must stay above 80% or PR check fails, enforced by codecov bot comment", Kind: "procedural", Tags: []string{"ci", "testing"}, Importance: 0.6},
	{NS: evalNS, Key: "ci-docker-build", Content: "Docker images built with multi-stage Dockerfile, alpine base, pushed to ghcr.io/org/api with git SHA tag", Kind: "procedural", Tags: []string{"ci", "deployment"}, Importance: 0.5},
	{NS: evalNS, Key: "ci-lint-config", Content: "golangci-lint config enables govet, staticcheck, errcheck, gosec. Runs in CI and pre-commit hook", Kind: "procedural", Tags: []string{"ci", "project:api"}, Importance: 0.4},

	// === Testing cluster (5) ===
	{NS: evalNS, Key: "test-integration-db", Content: "Integration tests use testcontainers to spin up PostgreSQL, Redis, and Elasticsearch per test suite", Kind: "procedural", Tags: []string{"testing", "project:api"}, Importance: 0.7},
	{NS: evalNS, Key: "test-mock-guidelines", Content: "Only mock external HTTP services and third-party APIs, never mock the database or internal packages", Kind: "semantic", Tags: []string{"testing", "convention"}, Importance: 0.8},
	{NS: evalNS, Key: "test-fixture-pattern", Content: "Test fixtures loaded from testdata/ directory as golden files, updated with -update flag", Kind: "procedural", Tags: []string{"testing", "project:api"}, Importance: 0.5},
	{NS: evalNS, Key: "test-flaky-retry", Content: "Flaky test detected in TestSearchRanking — intermittent failure due to Elasticsearch index refresh timing. Added 1s sleep as workaround.", Kind: "episodic", Tags: []string{"testing", "debugging"}, Importance: 0.6},
	{NS: evalNS, Key: "test-e2e-playwright", Content: "End-to-end tests use Playwright for browser automation, run nightly against staging environment", Kind: "semantic", Tags: []string{"testing", "project:frontend"}, Importance: 0.5},

	// === Go patterns cluster (5) ===
	{NS: evalNS, Key: "go-error-wrapping", Content: "Always wrap errors with fmt.Errorf and %w verb to preserve error chain for errors.Is/As checks", Kind: "procedural", Tags: []string{"go", "convention"}, Importance: 0.6},
	{NS: evalNS, Key: "go-context-timeout", Content: "All HTTP handlers must respect context cancellation. Use ctx.Done() in long-running loops", Kind: "procedural", Tags: []string{"go", "convention"}, Importance: 0.7},
	{NS: evalNS, Key: "go-struct-validation", Content: "Use go-playground/validator for struct validation at API boundary, not deep in business logic", Kind: "semantic", Tags: []string{"go", "convention"}, Importance: 0.5},
	{NS: evalNS, Key: "go-goroutine-leak", Content: "Found goroutine leak in webhook handler — started goroutines without context cancellation. Fixed by passing request context", Kind: "episodic", Tags: []string{"go", "debugging"}, Importance: 0.8},
	{NS: evalNS, Key: "go-dependency-injection", Content: "Use constructor injection for services, not global variables. Makes testing easier and dependencies explicit", Kind: "semantic", Tags: []string{"go", "convention"}, Importance: 0.6},

	// === Frontend cluster (5) ===
	{NS: evalNS, Key: "fe-react-query", Content: "React Query handles all API data fetching with automatic caching, refetching, and optimistic updates", Kind: "semantic", Tags: []string{"frontend", "project:frontend"}, Importance: 0.6},
	{NS: evalNS, Key: "fe-tailwind-theme", Content: "Tailwind config extends theme with brand colors: primary-500=#3B82F6, accent-500=#10B981. Dark mode uses class strategy", Kind: "procedural", Tags: []string{"frontend", "project:frontend"}, Importance: 0.4},
	{NS: evalNS, Key: "fe-nextjs-ssr", Content: "Product pages use SSR via getServerSideProps for SEO, dashboard pages use client-side rendering only", Kind: "semantic", Tags: []string{"frontend", "project:frontend"}, Importance: 0.6},
	{NS: evalNS, Key: "fe-form-validation", Content: "Forms use react-hook-form with zod schema validation. Error messages defined in shared validation schemas", Kind: "procedural", Tags: []string{"frontend", "project:frontend"}, Importance: 0.5},
	{NS: evalNS, Key: "fe-bundle-size", Content: "Bundle analyzer shows lodash contributing 70KB to main chunk. Replaced full import with lodash-es tree-shakeable imports", Kind: "episodic", Tags: []string{"frontend", "debugging"}, Importance: 0.6},

	// === Infrastructure cluster (5) ===
	{NS: evalNS, Key: "infra-terraform-modules", Content: "Terraform modules in infra/ repo: vpc, eks, rds, elasticache, s3. State stored in S3 with DynamoDB locking", Kind: "semantic", Tags: []string{"infrastructure", "ops"}, Importance: 0.6},
	{NS: evalNS, Key: "infra-aws-regions", Content: "Primary region us-east-1, disaster recovery in us-west-2. Cross-region replication for RDS and S3", Kind: "semantic", Tags: []string{"infrastructure", "ops"}, Importance: 0.7},
	{NS: evalNS, Key: "infra-cost-optimization", Content: "Switched to Graviton instances for 20% cost savings. Reserved instances for production RDS and ElastiCache", Kind: "episodic", Tags: []string{"infrastructure", "ops"}, Importance: 0.5},
	{NS: evalNS, Key: "infra-secrets-management", Content: "Secrets stored in AWS Secrets Manager, rotated every 90 days. Application reads via sidecar injector in k8s", Kind: "procedural", Tags: []string{"infrastructure", "security"}, Importance: 0.7},
	{NS: evalNS, Key: "infra-dns-config", Content: "DNS managed in Route53. API at api.example.com, staging at api-staging.example.com. CloudFront for static assets", Kind: "procedural", Tags: []string{"infrastructure", "ops"}, Importance: 0.4},

	// === Security cluster (4) ===
	{NS: evalNS, Key: "sec-rate-limiting", Content: "API rate limiting: 100 requests/minute per user for standard tier, 1000 for premium. Uses Redis sliding window", Kind: "procedural", Tags: []string{"security", "project:api"}, Importance: 0.7},
	{NS: evalNS, Key: "sec-cors-policy", Content: "CORS allows only app.example.com and localhost:3000. Credentials mode enabled for cookie-based auth", Kind: "procedural", Tags: []string{"security", "project:api"}, Importance: 0.6},
	{NS: evalNS, Key: "sec-sql-injection", Content: "All database queries must use parameterized queries. Found and fixed raw string interpolation in search endpoint", Kind: "episodic", Tags: []string{"security", "debugging"}, Importance: 0.9},
	{NS: evalNS, Key: "sec-dependency-audit", Content: "Dependabot enabled for all repos. Critical vulnerabilities must be patched within 48 hours per security policy", Kind: "procedural", Tags: []string{"security", "ops"}, Importance: 0.6},

	// === Performance cluster (4) ===
	{NS: evalNS, Key: "perf-api-caching", Content: "API responses cached in Redis with Cache-Control headers. GET endpoints use ETag-based conditional requests", Kind: "procedural", Tags: []string{"performance", "project:api"}, Importance: 0.6},
	{NS: evalNS, Key: "perf-n-plus-one", Content: "Discovered N+1 query in /api/v2/users endpoint. Fixed by using dataloader pattern with batched SQL queries", Kind: "episodic", Tags: []string{"performance", "debugging"}, Importance: 0.8},
	{NS: evalNS, Key: "perf-slow-query-log", Content: "PostgreSQL slow query log threshold set to 500ms. Weekly review meeting to address top 10 slowest queries", Kind: "procedural", Tags: []string{"performance", "database"}, Importance: 0.5},
	{NS: evalNS, Key: "perf-cdn-static", Content: "Static assets served via CloudFront CDN with 1-year cache and content-hash filenames for cache busting", Kind: "procedural", Tags: []string{"performance", "infrastructure"}, Importance: 0.4},

	// === Team/process noise (8) — diverse unrelated memories ===
	{NS: evalNS, Key: "recipe-pasta", Content: "Best pasta sauce uses San Marzano tomatoes simmered for 45 minutes with fresh basil", Kind: "procedural", Tags: []string{"cooking"}, Importance: 0.3},
	{NS: evalNS, Key: "meeting-notes-q4", Content: "Q4 planning: focus on API performance, defer mobile app to Q1", Kind: "episodic", Tags: []string{"planning"}, Importance: 0.4},
	{NS: evalNS, Key: "team-standup-format", Content: "Daily standup at 9:30am PST. Format: yesterday/today/blockers. Keep under 15 minutes", Kind: "procedural", Tags: []string{"process"}, Importance: 0.3},
	{NS: evalNS, Key: "onboarding-checklist", Content: "New hire checklist: GitHub access, AWS IAM, Slack channels, 1-on-1 with tech lead, PR review buddy", Kind: "procedural", Tags: []string{"process"}, Importance: 0.3},
	{NS: evalNS, Key: "book-recommendation", Content: "Designing Data-Intensive Applications by Martin Kleppmann is essential reading for backend engineers", Kind: "semantic", Tags: []string{"learning"}, Importance: 0.3},
	{NS: evalNS, Key: "conference-talk-idea", Content: "Proposed talk on event-driven architecture patterns for GopherCon 2025, abstract submitted", Kind: "episodic", Tags: []string{"career"}, Importance: 0.2},
	{NS: evalNS, Key: "office-wifi-password", Content: "Office WiFi network: CorpNet-5G, password: Welcome2024! Guest network: Guest-Open (no password)", Kind: "procedural", Tags: []string{"office"}, Importance: 0.2},
	{NS: evalNS, Key: "lunch-spots", Content: "Best lunch spots near office: Pho 99 (Vietnamese), Tacqueria El Rey (Mexican), Sweetgreen (salads)", Kind: "episodic", Tags: []string{"office"}, Importance: 0.1},
}

// evalTestCases define queries and which memories should be retrieved.
var evalTestCases = []evalTestCase{
	{
		Query:        "JWT authentication token security",
		ExpectedKeys: []string{"auth-jwt-signing", "auth-token-expiry", "auth-cookie-storage", "auth-session-bug"},
	},
	{
		Query:        "database migration safety",
		ExpectedKeys: []string{"db-migration-gotcha", "db-postgres-choice"},
	},
	{
		Query:        "deployment health checks kubernetes",
		ExpectedKeys: []string{"deploy-health-check", "deploy-k8s-rollout"},
	},
	{
		Query:        "JSONB indexing PostgreSQL",
		ExpectedKeys: []string{"db-indexing-strategy", "db-postgres-choice"},
	},
	{
		Query:        "refresh token cookie security",
		ExpectedKeys: []string{"auth-cookie-storage", "auth-session-bug", "auth-token-expiry"},
	},
}

// evalSession simulates a coding session that produces memories over time.
type evalSession struct {
	Name     string
	Memories []store.PutParams
	// After storing, optionally consolidate these keys under a summary
	ConsolidateKey     string
	ConsolidateContent string
	ConsolidateKeys    []string
}

// evalSimSessions simulate 3 multi-session coding scenarios.
// Memories are stored across sessions, then retrieval is tested
// to see if cross-session recall and consolidation work.
var evalSimSessions = []evalSession{
	{
		Name: "session-1-api-debugging",
		Memories: []store.PutParams{
			{NS: evalNS, Key: "s1-redis-nil-error", Content: "Redis GET returns nil for missing keys, not empty string. The Go redis client returns redis.Nil error which must be checked separately from other errors.", Kind: "semantic", Tags: []string{"debugging", "project:api", "session:s1"}, Importance: 0.7},
			{NS: evalNS, Key: "s1-redis-connection-pool", Content: "Redis connection pool was exhausted causing timeout errors. Increased MaxIdle from 10 to 50 and MaxActive from 100 to 500.", Kind: "episodic", Tags: []string{"debugging", "project:api", "session:s1"}, Importance: 0.6},
			{NS: evalNS, Key: "s1-api-rate-limiter", Content: "Rate limiter uses sliding window algorithm with Redis sorted sets. Key pattern: ratelimit:{user_id}:{endpoint}", Kind: "procedural", Tags: []string{"project:api", "session:s1"}, Importance: 0.5},
			{NS: evalNS, Key: "s1-user-complaint-timeout", Content: "User reported API timeout on /api/v2/search endpoint. Root cause was Redis connection pool exhaustion under load.", Kind: "episodic", Tags: []string{"debugging", "project:api", "session:s1"}, Importance: 0.6},
		},
	},
	{
		Name: "session-2-api-refactor",
		Memories: []store.PutParams{
			{NS: evalNS, Key: "s2-redis-cache-layer", Content: "Added a cache-aside pattern for /api/v2/search. Cache key includes query hash + pagination. TTL 5 minutes.", Kind: "semantic", Tags: []string{"project:api", "session:s2"}, Importance: 0.7},
			{NS: evalNS, Key: "s2-api-search-rewrite", Content: "Rewrote search endpoint to use Elasticsearch instead of PostgreSQL full-text search. 10x faster for complex queries.", Kind: "semantic", Tags: []string{"project:api", "session:s2"}, Importance: 0.8},
			{NS: evalNS, Key: "s2-elasticsearch-mapping", Content: "Elasticsearch index uses custom analyzer with edge_ngram tokenizer for autocomplete. Field mapping: title(text+keyword), body(text), tags(keyword array).", Kind: "procedural", Tags: []string{"project:api", "session:s2"}, Importance: 0.6},
		},
	},
	{
		Name: "session-3-unrelated-frontend",
		Memories: []store.PutParams{
			{NS: evalNS, Key: "s3-react-state-bug", Content: "React useState hook doesn't batch updates inside setTimeout. Use useReducer or wrap in startTransition for consistent state.", Kind: "semantic", Tags: []string{"debugging", "project:frontend", "session:s3"}, Importance: 0.6},
			{NS: evalNS, Key: "s3-tailwind-config", Content: "Tailwind config extends theme with custom colors: primary-500=#3B82F6, accent-500=#10B981. Dark mode uses class strategy.", Kind: "procedural", Tags: []string{"project:frontend", "session:s3"}, Importance: 0.4},
			{NS: evalNS, Key: "s3-nextjs-middleware", Content: "Next.js middleware runs at the edge. Cannot use Node.js APIs like fs or process.env directly. Use edge-compatible alternatives.", Kind: "semantic", Tags: []string{"project:frontend", "session:s3"}, Importance: 0.5},
		},
	},
}

// evalSimTestCases test cross-session retrieval after simulation.
var evalSimTestCases = []evalTestCase{
	{
		// Should recall redis debugging from session 1 AND cache layer from session 2
		Query:        "Redis connection issues and caching strategy",
		ExpectedKeys: []string{"s1-redis-nil-error", "s1-redis-connection-pool", "s2-redis-cache-layer"},
	},
	{
		// Should recall the full search evolution across sessions
		Query:        "API search endpoint performance",
		ExpectedKeys: []string{"s1-user-complaint-timeout", "s2-api-search-rewrite", "s2-elasticsearch-mapping"},
	},
	{
		// Should NOT return frontend memories for an API query
		Query:        "Redis rate limiting implementation",
		ExpectedKeys: []string{"s1-api-rate-limiter", "s1-redis-nil-error"},
	},
	{
		// Frontend-only query should not return API memories
		Query:        "React state management and Next.js",
		ExpectedKeys: []string{"s3-react-state-bug", "s3-nextjs-middleware"},
	},
	{
		// Cross-project query — should get the debugging memories from both
		Query:        "debugging timeout errors in production",
		ExpectedKeys: []string{"s1-user-complaint-timeout", "s1-redis-connection-pool"},
	},
}

func init() {
	evalCmd := &cobra.Command{
		Use:   "eval",
		Short: "Run retrieval quality evaluation",
		Long: `Seeds test memories in eval:ghost namespace, runs test queries,
and scores retrieval precision and recall. Use with --reflect to
run a reflect cycle and re-evaluate. Use with --simulate to run
fake multi-session coding scenarios. Use with --clean to remove
all eval data afterward.`,
		RunE: runEval,
	}

	evalCmd.Flags().Bool("reflect", false, "Run reflect cycle between seed and eval")
	evalCmd.Flags().Bool("simulate", false, "Simulate multi-session coding scenarios")
	evalCmd.Flags().Bool("clean", false, "Remove eval namespace after running")
	evalCmd.Flags().IntP("budget", "b", 2000, "Token budget for context queries")

	RootCmd.AddCommand(evalCmd)
}

func runEval(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	doReflect, _ := cmd.Flags().GetBool("reflect")
	doSimulate, _ := cmd.Flags().GetBool("simulate")
	doClean, _ := cmd.Flags().GetBool("clean")
	budget, _ := cmd.Flags().GetInt("budget")

	// Step 1: Seed test memories (idempotent — Put upserts)
	for _, p := range evalSeedMemories {
		if _, err := st.Put(ctx, p); err != nil {
			return fmt.Errorf("seed %s: %w", p.Key, err)
		}
	}

	// Step 1b: Optionally simulate multi-session coding scenarios
	if doSimulate {
		if err := runSimulation(ctx); err != nil {
			return fmt.Errorf("simulate: %w", err)
		}
	}

	// Step 2: Optionally run reflect to let edges/linking/consolidation happen
	reflected := false
	if doReflect {
		_, err := st.Reflect(ctx, store.ReflectParams{NS: evalNS})
		if err != nil {
			return fmt.Errorf("reflect: %w", err)
		}
		reflected = true
	}

	// Step 3: Run test queries and score
	// Use both base test cases and simulation test cases
	testCases := make([]evalTestCase, 0, len(evalTestCases)+len(evalSimTestCases))
	testCases = append(testCases, evalTestCases...)
	if doSimulate {
		testCases = append(testCases, evalSimTestCases...)
	}

	report := evalReport{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		NS:        evalNS,
		Reflected: reflected,
	}

	count, _ := st.MemoryCount(ctx)
	report.MemoryCount = count

	var totalPrecision, totalRecall float64

	for _, tc := range testCases {
		result := runEvalCase(ctx, tc, budget)
		report.Results = append(report.Results, result)
		totalPrecision += result.Precision
		totalRecall += result.Recall
	}

	report.TotalCases = len(testCases)
	if report.TotalCases > 0 {
		report.AvgPrecision = totalPrecision / float64(report.TotalCases)
		report.AvgRecall = totalRecall / float64(report.TotalCases)
	}

	// Step 4: Store eval result as a memory for trend tracking
	st.Put(ctx, store.PutParams{
		NS:         evalNS,
		Key:        fmt.Sprintf("eval-result-%d", time.Now().UnixMilli()),
		Content:    fmt.Sprintf("Eval run: avg_precision=%.2f avg_recall=%.2f cases=%d reflected=%v", report.AvgPrecision, report.AvgRecall, report.TotalCases, report.Reflected),
		Kind:       "episodic",
		Tags:       []string{"eval-result"},
		Importance: 0.3,
		TTL:        "30d",
	})

	// Step 5: Optionally clean up
	if doClean {
		st.RmNamespace(ctx, evalNS, true)
	}

	outputJSON(cmd, report)
	return nil
}

func runEvalCase(ctx context.Context, tc evalTestCase, budget int) evalResult {
	result := evalResult{
		Query:        tc.Query,
		ExpectedKeys: tc.ExpectedKeys,
	}

	contextResult, err := st.Context(ctx, store.ContextParams{
		NS:     evalNS,
		Query:  tc.Query,
		Tags:   tc.Tags,
		Budget: budget,
	})
	if err != nil {
		return result
	}

	expectedSet := map[string]bool{}
	for _, k := range tc.ExpectedKeys {
		expectedSet[k] = true
	}

	returnedSet := map[string]bool{}
	for _, m := range contextResult.Memories {
		result.ReturnedKeys = append(result.ReturnedKeys, m.Key)
		returnedSet[m.Key] = true
	}

	// Build a map of parent → children for consolidation-aware scoring.
	// If a returned key is a consolidation parent that contains expected children,
	// those children count as hits (parent summary replaces children in context).
	parentCovers := map[string][]string{} // returned parent key → expected child keys it covers
	for _, returnedKey := range result.ReturnedKeys {
		if expectedSet[returnedKey] {
			continue // direct hit, no need to check children
		}
		// Check if this returned key is a parent that contains any expected keys
		expandResult, err := st.Expand(ctx, store.ExpandParams{NS: evalNS, Key: returnedKey})
		if err != nil || expandResult == nil || len(expandResult.Children) == 0 {
			continue
		}
		for _, child := range expandResult.Children {
			if expectedSet[child.Key] {
				parentCovers[returnedKey] = append(parentCovers[returnedKey], child.Key)
			}
		}
	}

	// Calculate hits and misses (consolidation-aware)
	coveredByParent := map[string]bool{}
	for _, children := range parentCovers {
		for _, k := range children {
			coveredByParent[k] = true
		}
	}

	for _, k := range tc.ExpectedKeys {
		if returnedSet[k] || coveredByParent[k] {
			result.Hits = append(result.Hits, k)
		} else {
			result.Misses = append(result.Misses, k)
		}
	}

	// Precision: of returned, how many were expected or cover expected?
	if len(result.ReturnedKeys) > 0 {
		relevant := 0
		for _, k := range result.ReturnedKeys {
			if expectedSet[k] || len(parentCovers[k]) > 0 {
				relevant++
			}
		}
		result.Precision = float64(relevant) / float64(len(result.ReturnedKeys))
	}

	// Recall: of expected, how many were returned (directly or via parent)?
	if len(tc.ExpectedKeys) > 0 {
		result.Recall = float64(len(result.Hits)) / float64(len(tc.ExpectedKeys))
	}

	// Ensure nil slices become empty for JSON
	if result.Hits == nil {
		result.Hits = []string{}
	}
	if result.Misses == nil {
		result.Misses = []string{}
	}
	if result.ReturnedKeys == nil {
		result.ReturnedKeys = []string{}
	}

	return result
}

// runSimulation seeds fake multi-session coding memories and optionally
// consolidates them to test the full lifecycle.
func runSimulation(ctx context.Context) error {
	for _, session := range evalSimSessions {
		for _, p := range session.Memories {
			if _, err := st.Put(ctx, p); err != nil {
				return fmt.Errorf("simulate %s/%s: %w", session.Name, p.Key, err)
			}
		}

		// If session defines a consolidation, create it
		if session.ConsolidateKey != "" && len(session.ConsolidateKeys) >= 2 {
			_, err := st.Consolidate(ctx, store.ConsolidateParams{
				NS:         evalNS,
				SummaryKey: session.ConsolidateKey,
				Content:    session.ConsolidateContent,
				SourceKeys: session.ConsolidateKeys,
				Kind:       "semantic",
				Importance: 0.7,
			})
			if err != nil {
				return fmt.Errorf("consolidate %s: %w", session.ConsolidateKey, err)
			}
		}
	}
	return nil
}
