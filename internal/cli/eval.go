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
var evalSeedMemories = []store.PutParams{
	// Auth cluster
	{NS: evalNS, Key: "auth-jwt-signing", Content: "Authentication uses JWT tokens with RSA256 signing for API access", Kind: "semantic", Tags: []string{"auth", "project:api"}, Importance: 0.7},
	{NS: evalNS, Key: "auth-token-expiry", Content: "JWT access tokens expire after 15 minutes, refresh tokens after 7 days", Kind: "semantic", Tags: []string{"auth", "project:api"}, Importance: 0.6},
	{NS: evalNS, Key: "auth-cookie-storage", Content: "Refresh tokens are stored in httpOnly secure cookies, not localStorage", Kind: "semantic", Tags: []string{"auth", "security"}, Importance: 0.8},
	{NS: evalNS, Key: "auth-session-bug", Content: "Session tokens were stored in plaintext cookies causing security audit failure", Kind: "episodic", Tags: []string{"auth", "debugging"}, Importance: 0.7},

	// Database cluster
	{NS: evalNS, Key: "db-postgres-choice", Content: "Chose PostgreSQL over MySQL for JSONB support and better concurrent write performance", Kind: "semantic", Tags: []string{"database", "project:api"}, Importance: 0.8},
	{NS: evalNS, Key: "db-migration-gotcha", Content: "Always run migrations in a transaction, we lost data once when a migration failed halfway", Kind: "episodic", Tags: []string{"database", "debugging"}, Importance: 0.9},
	{NS: evalNS, Key: "db-indexing-strategy", Content: "Use GIN indexes for JSONB columns and B-tree for UUID primary keys", Kind: "procedural", Tags: []string{"database", "project:api"}, Importance: 0.6},

	// Deployment cluster
	{NS: evalNS, Key: "deploy-k8s-rollout", Content: "Use rolling deployment strategy with maxSurge=1 and maxUnavailable=0 for zero downtime", Kind: "procedural", Tags: []string{"deployment", "project:api"}, Importance: 0.7},
	{NS: evalNS, Key: "deploy-health-check", Content: "Health check endpoint must return 200 within 5 seconds or pod gets killed by liveness probe", Kind: "procedural", Tags: []string{"deployment", "debugging"}, Importance: 0.8},

	// Unrelated memories (noise)
	{NS: evalNS, Key: "recipe-pasta", Content: "Best pasta sauce uses San Marzano tomatoes simmered for 45 minutes with fresh basil", Kind: "procedural", Tags: []string{"cooking"}, Importance: 0.3},
	{NS: evalNS, Key: "meeting-notes-q4", Content: "Q4 planning: focus on API performance, defer mobile app to Q1", Kind: "episodic", Tags: []string{"planning"}, Importance: 0.4},
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

func init() {
	evalCmd := &cobra.Command{
		Use:   "eval",
		Short: "Run retrieval quality evaluation",
		Long: `Seeds test memories in eval:ghost namespace, runs test queries,
and scores retrieval precision and recall. Use with --reflect to
run a reflect cycle and re-evaluate. Use with --clean to remove
all eval data afterward.`,
		RunE: runEval,
	}

	evalCmd.Flags().Bool("reflect", false, "Run reflect cycle between seed and eval")
	evalCmd.Flags().Bool("clean", false, "Remove eval namespace after running")
	evalCmd.Flags().IntP("budget", "b", 2000, "Token budget for context queries")

	RootCmd.AddCommand(evalCmd)
}

func runEval(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	doReflect, _ := cmd.Flags().GetBool("reflect")
	doClean, _ := cmd.Flags().GetBool("clean")
	budget, _ := cmd.Flags().GetInt("budget")

	// Step 1: Seed test memories (idempotent — Put upserts)
	for _, p := range evalSeedMemories {
		if _, err := st.Put(ctx, p); err != nil {
			return fmt.Errorf("seed %s: %w", p.Key, err)
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
	report := evalReport{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		NS:        evalNS,
		Reflected: reflected,
	}

	count, _ := st.MemoryCount(ctx)
	report.MemoryCount = count

	var totalPrecision, totalRecall float64

	for _, tc := range evalTestCases {
		result := runEvalCase(ctx, tc, budget)
		report.Results = append(report.Results, result)
		totalPrecision += result.Precision
		totalRecall += result.Recall
	}

	report.TotalCases = len(evalTestCases)
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

	// Calculate hits and misses
	for _, k := range tc.ExpectedKeys {
		if returnedSet[k] {
			result.Hits = append(result.Hits, k)
		} else {
			result.Misses = append(result.Misses, k)
		}
	}

	// Precision: of returned, how many were expected?
	if len(result.ReturnedKeys) > 0 {
		relevant := 0
		for _, k := range result.ReturnedKeys {
			if expectedSet[k] {
				relevant++
			}
		}
		result.Precision = float64(relevant) / float64(len(result.ReturnedKeys))
	}

	// Recall: of expected, how many were returned?
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
