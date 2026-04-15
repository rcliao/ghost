// Package mcpserver implements an MCP (Model Context Protocol) server for ghost.
// It exposes ghost's memory operations as MCP tools over stdio transport.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rcliao/ghost/internal/model"
	"github.com/rcliao/ghost/internal/store"
)

const serverInstructions = `Ghost is a persistent memory system. Use it to store and recall knowledge across sessions.

When to use:
- Store a memory when you learn something worth remembering: user preferences, project decisions, debugging insights, or corrections the user makes.
- Search memories when you need context from past sessions or when the user references something you should already know.

Namespace conventions:
- agent:<name> — per-agent memory space (e.g. agent:pikamini, agent:coder)
- Each agent's memories are isolated — no cross-namespace visibility.

Tags (first-class filtering, use for categorization):
- identity — core agent persona (name, personality, appearance)
- lore — background knowledge, relationships, trivia
- chat:<id> — per-conversation context
- project:<name> — project-specific knowledge
- learning — accumulated insights
- convention — coding/writing rules
- user:<name> — per-user preferences

Memory kinds (Tulving's taxonomy, affects retrieval scoring):
- episodic — events/experiences, scored with recency bias (default for sensory/stm tier)
- semantic — facts/knowledge, scored with relevance + importance bias (default for ltm tier)
- procedural — how-to/skills/steps, scored with access frequency bias (practice effect)

Priority: low, normal (default), high, critical.
Tier (Atkinson-Shiffrin model): sensory (ultra-short, aggressive decay), stm (default, subject to decay), ltm (proven useful, long-term).
Pinned: set pinned=true for memories that should always be loaded in context (e.g. identity, core conventions). Pinned memories are exempt from lifecycle decay.

Retrieval flow:
- ghost_context: start here — assembles scored context within a token budget. Summaries replace their children automatically. When compaction_suggested is true, use ghost_expand to find what needs consolidation.
- ghost_search: use for specific recall when you know what you're looking for.
- ghost_expand: with no key, lists all consolidation nodes AND emergent clusters needing consolidation. With a key, drills into a summary to get its children (children with children>0 are expandable further).
- ghost_get: retrieve a specific memory by key when you already know it.
- ghost_consolidate: create a summary that groups related memories. Children are suppressed in future context calls.

Consolidation workflow (when compaction_suggested is true):
1. ghost_expand(ns) — see existing nodes + clusters needing consolidation
2. For each cluster: ghost_get each key to read content, write a summary
3. ghost_consolidate(ns, summary_key, content, source_keys) — create parent node

Utility feedback: when a memory helps you solve a problem or answer a question, boost it:
  ghost_curate(ns, key, op="boost")
This increases importance by 0.2, making it rank higher in future context assembly.
When a memory is wrong or outdated, diminish or archive it:
  ghost_curate(ns, key, op="diminish") or ghost_curate(ns, key, op="archive")

When working with tool results, write down any important information you might need later in your response, as the original tool result may be cleared later.`

// Serve starts the MCP server on stdio, blocking until the connection closes.
func Serve(ctx context.Context, st store.Store) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ghost",
		Version: "1.1.0",
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})

	registerTools(server, st)

	transport := &mcp.StdioTransport{}
	return server.Run(ctx, transport)
}

// schema builds a JSON Schema object for tool InputSchema.
func schema(required []string, props map[string]map[string]any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func registerTools(server *mcp.Server, st store.Store) {
	server.AddTool(&mcp.Tool{
		Name:        "ghost_put",
		Description: "Store or update a memory. Storing to an existing namespace:key creates a new version.",
		InputSchema: schema([]string{"ns", "key", "content"}, map[string]map[string]any{
			"ns":         prop("string", "Namespace (agent identity), e.g. agent:pikamini, agent:coder"),
			"key":        prop("string", "Unique descriptive key within the namespace"),
			"content":    prop("string", "Memory content text"),
			"kind":       prop("string", "Memory kind: episodic (events, default for sensory/stm), semantic (facts, default for ltm), or procedural (how-to/skills)"),
			"tags":       {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags for categorization (e.g. identity, lore, project:ghost, chat:123)"},
			"priority":   prop("string", "Priority: low, normal (default), high, critical"),
			"importance": prop("number", "Importance score 0.0-1.0 (default 0.5)"),
			"tier":       prop("string", "Storage tier: sensory (ultra-short), stm (default), ltm (proven useful)"),
			"pinned":     prop("boolean", "If true, always loaded in context and exempt from lifecycle decay"),
			"ttl":        prop("string", "Time-to-live, e.g. 7d, 24h, 30m"),
			"dedup":      prop("boolean", "If true, skip storing when a semantically similar memory already exists (cosine > 0.82)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS         string   `json:"ns"`
			Key        string   `json:"key"`
			Content    string   `json:"content"`
			Kind       string   `json:"kind"`
			Tags       []string `json:"tags"`
			Priority   string   `json:"priority"`
			Importance float64  `json:"importance"`
			Tier       string   `json:"tier"`
			Pinned     bool     `json:"pinned"`
			TTL        string   `json:"ttl"`
			Dedup      bool     `json:"dedup"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.Key == "" || p.Content == "" {
			return errResult("ns, key, and content are required"), nil
		}
		mem, err := st.Put(ctx, store.PutParams{
			NS:         p.NS,
			Key:        p.Key,
			Content:    p.Content,
			Kind:       p.Kind,
			Tags:       p.Tags,
			Priority:   p.Priority,
			Importance: p.Importance,
			Tier:       p.Tier,
			Pinned:     p.Pinned,
			TTL:        p.TTL,
			Dedup:      p.Dedup,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		// Signal dedup to caller: if requested key differs from returned key,
		// a similar memory already existed and was returned instead
		if p.Dedup && mem.Key != p.Key {
			type putResult struct {
				*model.Memory
				Deduplicated    bool   `json:"deduplicated"`
				RequestedKey    string `json:"requested_key"`
			}
			return jsonResult(putResult{Memory: mem, Deduplicated: true, RequestedKey: p.Key})
		}
		return jsonResult(mem)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_search",
		Description: "Search memories by content using full-text search with ranking. Use this to recall knowledge from past sessions.",
		InputSchema: schema([]string{"query"}, map[string]map[string]any{
			"query":  prop("string", "Search query text"),
			"ns":     prop("string", "Namespace filter (optional)"),
			"kind":   prop("string", "Filter by kind: semantic, episodic, procedural"),
			"tags":   {"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by tags (e.g. identity, project:ghost)"},
			"limit":  prop("integer", "Max results (default 20)"),
			"after":  prop("string", "Only memories created after this date (YYYY-MM-DD or RFC3339)"),
			"before": prop("string", "Only memories created before this date (YYYY-MM-DD or RFC3339)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			Query  string   `json:"query"`
			NS     string   `json:"ns"`
			Kind   string   `json:"kind"`
			Tags   []string `json:"tags"`
			Limit  int      `json:"limit"`
			After  string   `json:"after"`
			Before string   `json:"before"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.Query == "" {
			return errResult("query is required"), nil
		}
		sp := store.SearchParams{
			NS:    p.NS,
			Query: p.Query,
			Kind:  p.Kind,
			Tags:  p.Tags,
			Limit: p.Limit,
		}
		if p.After != "" {
			if t, err := parseDate(p.After); err == nil {
				sp.After = t
			}
		}
		if p.Before != "" {
			if t, err := parseDate(p.Before); err == nil {
				sp.Before = t
			}
		}
		results, err := st.Search(ctx, sp)
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(results)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_context",
		Description: "Assemble the most relevant memories for a task within a token budget, scored by relevance, recency, importance, and access frequency. Use this when starting a task that may benefit from past context.",
		InputSchema: schema([]string{"query"}, map[string]map[string]any{
			"query":          prop("string", "Natural language description of the current task"),
			"ns":             prop("string", "Namespace filter (optional)"),
			"kind":           prop("string", "Filter by kind: semantic, episodic, procedural"),
			"tags":           {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tag filters"},
			"budget":         prop("integer", "Max tokens in output (default 4000)"),
			"exclude_pinned": prop("boolean", "Skip pinned memories, use full budget for search-ranked results"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			Query         string   `json:"query"`
			NS            string   `json:"ns"`
			Kind          string   `json:"kind"`
			Tags          []string `json:"tags"`
			Budget        int      `json:"budget"`
			ExcludePinned bool     `json:"exclude_pinned"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.Query == "" {
			return errResult("query is required"), nil
		}
		result, err := st.Context(ctx, store.ContextParams{
			NS:            p.NS,
			Query:         p.Query,
			Kind:          p.Kind,
			Tags:          p.Tags,
			Budget:        p.Budget,
			ExcludePinned: p.ExcludePinned,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_curate",
		Description: "Apply a lifecycle action to a single memory. Use this to directly promote, demote, boost, diminish, archive, delete, pin, or unpin a specific memory by namespace and key.",
		InputSchema: schema([]string{"ns", "key", "op"}, map[string]map[string]any{
			"ns":  prop("string", "Namespace of the memory"),
			"key": prop("string", "Key of the memory"),
			"op":  prop("string", "Action: promote (tier up), demote (tier down), boost (importance +0.2), diminish (importance -0.2), archive (→dormant), delete (soft-delete), pin (always in context), unpin (remove pin)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS  string `json:"ns"`
			Key string `json:"key"`
			Op  string `json:"op"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.Key == "" || p.Op == "" {
			return errResult("ns, key, and op are required"), nil
		}
		result, err := st.Curate(ctx, store.CurateParams{
			NS:  p.NS,
			Key: p.Key,
			Op:  p.Op,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_edge",
		Description: "Create, remove, or list weighted edges (associations) between memories. Edges enable DAG-based retrieval — when a seed memory is found, its neighbors are pulled in via spreading activation.",
		InputSchema: schema([]string{"ns", "from_key"}, map[string]map[string]any{
			"ns":       prop("string", "Namespace (used for both from and to)"),
			"from_key": prop("string", "Source memory key"),
			"to_key":   prop("string", "Target memory key (required for create/remove)"),
			"rel":      prop("string", "Relation type: relates_to, contradicts, depends_on, refines, contains, merged_into, caused_by, prevents, implies"),
			"weight":   prop("number", "Edge weight 0.0-1.0 (0 = use default for rel type)"),
			"op":       prop("string", "Operation: create (default), remove, list"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS      string  `json:"ns"`
			FromKey string  `json:"from_key"`
			ToKey   string  `json:"to_key"`
			Rel     string  `json:"rel"`
			Weight  float64 `json:"weight"`
			Op      string  `json:"op"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.FromKey == "" {
			return errResult("ns and from_key are required"), nil
		}

		op := p.Op
		if op == "" {
			op = "create"
		}

		switch op {
		case "list":
			edges, err := st.GetEdgesByNSKey(ctx, p.NS, p.FromKey)
			if err != nil {
				return errResult(err.Error()), nil
			}
			if edges == nil {
				edges = []store.Edge{}
			}
			return jsonResult(edges)

		case "remove":
			if p.ToKey == "" || p.Rel == "" {
				return errResult("to_key and rel are required for remove"), nil
			}
			err := st.DeleteEdge(ctx, store.EdgeParams{
				FromNS: p.NS, FromKey: p.FromKey,
				ToNS: p.NS, ToKey: p.ToKey,
				Rel: p.Rel,
			})
			if err != nil {
				return errResult(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "deleted", "rel": p.Rel})

		case "create":
			if p.ToKey == "" || p.Rel == "" {
				return errResult("to_key and rel are required for create"), nil
			}
			edge, err := st.CreateEdge(ctx, store.EdgeParams{
				FromNS: p.NS, FromKey: p.FromKey,
				ToNS: p.NS, ToKey: p.ToKey,
				Rel: p.Rel, Weight: p.Weight,
			})
			if err != nil {
				return errResult(err.Error()), nil
			}
			return jsonResult(edge)

		default:
			return errResult("invalid op: must be create, remove, or list"), nil
		}
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_get",
		Description: "Retrieve a specific memory by namespace and key. Use this when you know exactly which memory you want.",
		InputSchema: schema([]string{"ns", "key"}, map[string]map[string]any{
			"ns":  prop("string", "Namespace (e.g. agent:pikamini)"),
			"key": prop("string", "Memory key"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS  string `json:"ns"`
			Key string `json:"key"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.Key == "" {
			return errResult("ns and key are required"), nil
		}
		results, err := st.Get(ctx, store.GetParams{NS: p.NS, Key: p.Key})
		if err != nil {
			return errResult(err.Error()), nil
		}
		if len(results) == 0 {
			return errResult("memory not found: " + p.NS + "/" + p.Key), nil
		}
		return jsonResult(results[0])
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_expand",
		Description: "Drill into the consolidation hierarchy. With a key: returns the summary and its children (children with children>0 are expandable further). Without a key: lists all consolidation nodes AND emergent clusters needing consolidation — use this when compaction_suggested is true to find what to consolidate.",
		InputSchema: schema([]string{"ns"}, map[string]map[string]any{
			"ns":  prop("string", "Namespace (e.g. agent:pikamini)"),
			"key": prop("string", "Key of a consolidation node to expand (omit to list all nodes + clusters)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS  string `json:"ns"`
			Key string `json:"key"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" {
			return errResult("ns is required"), nil
		}
		result, err := st.Expand(ctx, store.ExpandParams{NS: p.NS, Key: p.Key})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_consolidate",
		Description: "Create a summary memory that consolidates multiple source memories. Creates the summary and contains edges in one operation. Children are automatically suppressed in context when the summary is present.",
		InputSchema: schema([]string{"ns", "summary_key", "content", "source_keys"}, map[string]map[string]any{
			"ns":          prop("string", "Namespace (e.g. agent:pikamini)"),
			"summary_key": prop("string", "Key for the new summary memory"),
			"content":     prop("string", "Summary content text (caller must provide — no LLM inside ghost)"),
			"source_keys": {"type": "array", "items": map[string]any{"type": "string"}, "description": "Keys of memories to consolidate (minimum 2)"},
			"kind":        prop("string", "Memory kind for summary (default: semantic)"),
			"importance":  prop("number", "Importance 0.0-1.0 (default: 0.7)"),
			"tags":        {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags for the summary memory"},
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS         string   `json:"ns"`
			SummaryKey string   `json:"summary_key"`
			Content    string   `json:"content"`
			SourceKeys []string `json:"source_keys"`
			Kind       string   `json:"kind"`
			Importance float64  `json:"importance"`
			Tags       []string `json:"tags"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.SummaryKey == "" || p.Content == "" || len(p.SourceKeys) < 2 {
			return errResult("ns, summary_key, content, and at least 2 source_keys are required"), nil
		}
		result, err := st.Consolidate(ctx, store.ConsolidateParams{
			NS:         p.NS,
			SummaryKey: p.SummaryKey,
			Content:    p.Content,
			SourceKeys: p.SourceKeys,
			Kind:       p.Kind,
			Importance: p.Importance,
			Tags:       p.Tags,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_reflect",
		Description: "Run the reflect cycle to promote, decay, demote, archive, delete, or merge similar memories based on lifecycle rules. Includes automatic similarity-based deduplication using embedding vectors. Call this to maintain memory hygiene — especially when ghost_context indicates compaction is needed (compaction_suggested: true).",
		InputSchema: schema([]string{}, map[string]map[string]any{
			"ns":      prop("string", "Namespace filter (optional, empty = all namespaces)"),
			"dry_run": prop("boolean", "If true, preview what would happen without applying changes"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS     string `json:"ns"`
			DryRun bool   `json:"dry_run"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		result, err := st.Reflect(ctx, store.ReflectParams{
			NS:     p.NS,
			DryRun: p.DryRun,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})
}

func unmarshalArgs(req *mcp.CallToolRequest, v any) error {
	b, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return errResult(err.Error()), nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
		}, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: buf.String()}},
	}, nil
}

func parseDate(s string) (time.Time, error) {
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date: %s", s)
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "error: " + msg}},
		IsError: true,
	}
}
