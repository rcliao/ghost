// Package mcpserver implements an MCP (Model Context Protocol) server for ghost.
// It exposes ghost's memory operations as MCP tools over stdio transport.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(mem)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_search",
		Description: "Search memories by content using full-text search with ranking. Use this to recall knowledge from past sessions.",
		InputSchema: schema([]string{"query"}, map[string]map[string]any{
			"query": prop("string", "Search query text"),
			"ns":    prop("string", "Namespace filter (optional)"),
			"kind":  prop("string", "Filter by kind: semantic, episodic, procedural"),
			"tags":  {"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by tags (e.g. identity, project:ghost)"},
			"limit": prop("integer", "Max results (default 20)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			Query string   `json:"query"`
			NS    string   `json:"ns"`
			Kind  string   `json:"kind"`
			Tags  []string `json:"tags"`
			Limit int      `json:"limit"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.Query == "" {
			return errResult("query is required"), nil
		}
		results, err := st.Search(ctx, store.SearchParams{
			NS:    p.NS,
			Query: p.Query,
			Kind:  p.Kind,
			Tags:  p.Tags,
			Limit: p.Limit,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(results)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_context",
		Description: "Assemble the most relevant memories for a task within a token budget, scored by relevance, recency, importance, and access frequency. Use this when starting a task that may benefit from past context.",
		InputSchema: schema([]string{"query"}, map[string]map[string]any{
			"query":  prop("string", "Natural language description of the current task"),
			"ns":     prop("string", "Namespace filter (optional)"),
			"kind":   prop("string", "Filter by kind: semantic, episodic, procedural"),
			"tags":   {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tag filters"},
			"budget": prop("integer", "Max tokens in output (default 4000)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			Query  string   `json:"query"`
			NS     string   `json:"ns"`
			Kind   string   `json:"kind"`
			Tags   []string `json:"tags"`
			Budget int      `json:"budget"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.Query == "" {
			return errResult("query is required"), nil
		}
		result, err := st.Context(ctx, store.ContextParams{
			NS:     p.NS,
			Query:  p.Query,
			Kind:   p.Kind,
			Tags:   p.Tags,
			Budget: p.Budget,
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
		Name:        "ghost_reflect",
		Description: "Run the reflect cycle to promote, decay, demote, archive, or delete memories based on lifecycle rules. Call this to maintain memory hygiene — especially when ghost_context indicates compaction is needed (compaction_suggested: true).",
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

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "error: " + msg}},
		IsError: true,
	}
}
