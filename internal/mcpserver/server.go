// Package mcpserver implements an MCP (Model Context Protocol) server for ghost.
// It exposes ghost's memory operations as MCP tools over stdio transport.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rcliao/ghost/internal/model"
	"github.com/rcliao/ghost/internal/store"
)

// ServerInstructions is the guidance every MCP agent receives at session
// start. Exported so the agent-guidance eval grades the EXACT text production
// serves — a copy would drift.
const ServerInstructions = `Ghost is a persistent memory system. Use it to store and recall knowledge across sessions.

When to use:
- Store a memory when you learn something worth remembering: user preferences, project decisions, debugging insights, or corrections the user makes.
- Search memories when you need context from past sessions or when the user references something you should already know.

Namespace conventions:
- agent:<name> — per-agent memory space (e.g. agent:pikamini, agent:coder)
- Each agent's memories are isolated — no cross-namespace visibility.

Tags (categorization; in ghost_context they boost matching memories by default and filter only with tag_mode=filter; ghost_search still filters):
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
Provenance: when a memory records what a PERSON said, prefers, or did, set source_user to that person and source_kind to how you know it (stated | observed | self | peer). A stated preference outranks your own inference about the same person. The chat is a channel, not provenance — keep chat ids in tags. Use ONE canonical short id per person (the same string every time, e.g. "mami" — never a display name, nickname variant, or different casing); declared aliases (ghost source alias) make variant spellings resolve to the canonical, but a consistent id needs no repair.

Retrieval flow:
- ghost_context: start here — assembles scored context within a token budget. Summaries replace their children automatically. When compaction_suggested is true, use ghost_expand to find what needs consolidation.
- ghost_search: use for specific recall when you know what you're looking for.
- ghost_expand: with no key, lists all consolidation nodes AND emergent clusters needing consolidation. With a key, drills into a summary to get its children (children with children>0 are expandable further).
- ghost_get: retrieve a specific memory by key when you already know it.
- ghost_consolidate: create a summary that groups related memories. Children are suppressed in future context calls.

Relationships (edges) — the most under-used part of ghost:
Memories are a graph, not a list. When a seed memory is retrieved, its neighbours are pulled in with it, so an edge is how you make sure a fact arrives WITH the thing that qualifies it. Three relations reserve budget and still arrive when context is tight; reach for them when the neighbour's absence would make you state something FALSE:
- contradicts — the newer, correcting memory points at the stale one. Without it you assert a corrected fact as current.
- depends_on  — the task points at its PREREQUISITE. Without it you recommend an action that will fail.
- prevents    — the thing being ruled out points at the CONSTRAINT ruling it out (allergies, restrictions, hard rules). TRIGGER: whenever you STORE a plan or intention (meals, purchases, outings, scheduling), check your standing constraints; if one bears on it, create the prevents edge in the same breath as the put. A plan stored without its constraint is how you later suggest exactly the thing someone must avoid.
caused_by / implies / refines are surfaced when there is room. relates_to is auto-created for you — never hand-write it, and never label ordinary "same topic" similarity as a typed relation, because that takes reserved budget from real search results.
Direction is not cosmetic: an edge written the wrong way round is stored, looks correct, and is silently ignored during retrieval. Read it as a sentence, "from <rel> to".
- ghost_edge: create one edge you already know about.
- ghost_edge_candidates: ghost lists similar pairs that have no typed edge yet; you classify each one and commit the ones that warrant it. This is the intended way to build the graph — ghost does no LLM work itself.

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

// EdgeToolDescription is the ghost_edge tool description agents see.
// Exported so the agent-guidance eval grades the exact production text.
const EdgeToolDescription = "Create, remove, or list weighted edges (associations) between memories. Edges enable DAG-based retrieval — when a seed memory is found, its neighbours are pulled in via spreading activation.\n\n" +
	"DIRECTION IS NOT COSMETIC. An edge written the wrong way round is silently inert: it is stored, it looks correct in `list`, and it never influences retrieval. Nothing warns you. Read the edge as a sentence 'from <rel> to':\n" +
	"  from --contradicts--> to  the NEW/correcting memory points at the stale one it refutes\n" +
	"  from --refines-->     to  the LATER, more precise memory points at the one it improves\n" +
	"  from --depends_on-->  to  the dependent task points at its PREREQUISITE\n" +
	"  from --caused_by-->   to  the symptom points at its CAUSE\n" +
	"  from --prevents-->    to  the thing being ruled out points at the CONSTRAINT that rules it out\n" +
	"  from --implies-->     to  the premise points at its CONSEQUENCE\n" +
	"  from --contains-->    to  the parent summary points at each CHILD it covers\n" +
	"  relates_to is symmetric; merged_into is a dedup audit trail and is never traversed.\n\n" +
	"WHICH RELATION TO REACH FOR. contradicts, depends_on and prevents are the high-value ones: retrieval reserves budget for them, so they still reach the agent when context is tight. Use them when their ABSENCE would make you state something false — a corrected fact asserted as current, an action recommended without its blocker, a suggestion that violates a standing constraint (allergies, restrictions, hard rules). caused_by/implies/refines are surfaced when there is room. Do not reach for a typed relation when 'these two are about the same topic' is the truth — that is relates_to, and mislabelling it takes reserved budget away from real search results.\n\n" +
	"DEPTH: chains are followed two links deep for depends_on, prevents and caused_by — so 'book the dentist' -> 'the card lapsed' -> 'the form is unsigned' reaches the unsigned form, which is the only actionable part. Everything else, including relates_to and contradicts, is single-hop: what you want is the contradiction of a fact in context, not the contradiction of that contradiction. Beyond two links nothing is reached, so for a long chain link the memory a query will actually find directly to the blocker that matters."

// PatchToolDescription is the ghost_patch tool description agents see.
const PatchToolDescription = "Edit a memory by applying a unified diff to its current content — PREFER THIS OVER ghost_put WHEN EDITING an existing memory. Only the touched lines change; everything else survives byte-for-byte, so repeated edits cannot slowly reword the parts you did not mean to change (whole-value rewrites regenerate every sentence and drift). Two guards fail loudly instead of corrupting: hunks whose context lines do not match the current content verbatim are refused (category context_mismatch — re-read with ghost_get, regenerate the diff, retry), and the write lands only if the head is still the version you patched (category version_conflict — a concurrent writer got there first; re-read and retry). Workflow: ghost_get to read content+version -> generate a standard unified diff (@@ hunks, space/-/+ lines) -> ghost_patch, optionally with dry_run:true first to preview the result without writing."

// Serve starts the MCP server on stdio, blocking until the connection closes.
func Serve(ctx context.Context, st store.Store) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ghost",
		Version: "1.2.0",
	}, &mcp.ServerOptions{
		Instructions: ServerInstructions,
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
			"ns":           prop("string", "Namespace (agent identity), e.g. agent:pikamini, agent:coder"),
			"key":          prop("string", "Unique descriptive key within the namespace"),
			"content":      prop("string", "Memory content text"),
			"kind":         prop("string", "Memory kind: episodic (events, default for sensory/stm), semantic (facts, default for ltm), or procedural (how-to/skills)"),
			"tags":         {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags for categorization (e.g. identity, lore, project:ghost, chat:123)"},
			"priority":     prop("string", "Priority: low, normal (default), high, critical"),
			"importance":   prop("number", "Importance score 0.0-1.0 (default 0.5)"),
			"tier":         prop("string", "Storage tier: sensory (ultra-short), stm (default), ltm (proven useful)"),
			"pinned":       prop("boolean", "If true, always loaded in context and exempt from lifecycle decay"),
			"ttl":          prop("string", "Time-to-live, e.g. 7d, 24h, 30m"),
			"dedup":        prop("boolean", "If true, skip storing when a semantically similar memory already exists (cosine > 0.82)"),
			"source_user":  prop("string", "The PERSON this memory originated from (e.g. the family member who said it). NOT the chat — a chat is a channel and belongs in tags. Set this whenever a memory records what someone said, prefers, or did; it is how you later fit responses to that person and how junk stays attributable."),
			"source_kind":  prop("string", "How it entered the store: 'stated' (the person explicitly said it — highest authority), 'observed' (you inferred it from behaviour), 'self' (your own note to yourself), 'peer' (came from another agent)"),
			"source_scope": prop("string", "WHERE this memory was born — a stable place id like 'project:ghost' (coding agents: the project you are working in). Not the chat id (that is a tag). Use the same id every time; declared aliases repair variants but consistency needs no repair."),
			"base_version": prop("number", "Compare-and-swap: the version you read before editing (from ghost_get). The write fails with a version-conflict error if another writer moved the key past it — re-read, remake your edit against the fresh content, retry. Omit or 0 for an unconditional write. Use this whenever you EDIT an existing memory rather than append a new one."),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS          string   `json:"ns"`
			Key         string   `json:"key"`
			Content     string   `json:"content"`
			Kind        string   `json:"kind"`
			Tags        []string `json:"tags"`
			Priority    string   `json:"priority"`
			Importance  float64  `json:"importance"`
			Tier        string   `json:"tier"`
			Pinned      bool     `json:"pinned"`
			TTL         string   `json:"ttl"`
			Dedup       bool     `json:"dedup"`
			BaseVer     float64  `json:"base_version"`
			SourceUser  string   `json:"source_user"`
			SourceKind  string   `json:"source_kind"`
			SourceScope string   `json:"source_scope"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.Key == "" || p.Content == "" {
			return errResult("ns, key, and content are required"), nil
		}
		mem, err := st.Put(ctx, store.PutParams{
			NS:          p.NS,
			Key:         p.Key,
			Content:     p.Content,
			Kind:        p.Kind,
			Tags:        p.Tags,
			Priority:    p.Priority,
			Importance:  p.Importance,
			Tier:        p.Tier,
			Pinned:      p.Pinned,
			TTL:         p.TTL,
			Dedup:       p.Dedup,
			BaseVersion: int(p.BaseVer),
			SourceUser:  p.SourceUser,
			SourceKind:  p.SourceKind,
			SourceScope: p.SourceScope,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		// Signal dedup to caller: if requested key differs from returned key,
		// a similar memory already existed and was returned instead
		if p.Dedup && mem.Key != p.Key {
			type putResult struct {
				*model.Memory
				Deduplicated bool   `json:"deduplicated"`
				RequestedKey string `json:"requested_key"`
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
			"tags":           {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags. By default they BOOST matching memories (x1.8) and exclude nothing, so a relevant memory without the tag still surfaces; set tag_mode=filter to require every tag"},
			"tag_mode":       prop("string", "boost (default) | filter"),
			"budget":         prop("integer", "Max tokens in output (default 4000)"),
			"exclude_pinned": prop("boolean", "Skip pinned memories, use full budget for search-ranked results"),
			"min_score":      prop("number", "Absolute score floor (0-1). Drop candidates below this. 0 = no filter. Helpful at scale to suppress low-confidence retrievals."),
			"min_spread":     prop("number", "If top-1 score minus top-5 score is less than this delta, collapse to top-1 only (flat-noise detection). 0 = no filter. ~0.15 is a good starting value."),
			"for_user":       prop("string", "The person you are currently talking to. Memories they originated (source_user match) get a boost so their own stated facts and preferences surface first under a tight budget. A boost, never a filter — other people's facts stay retrievable."),
			"for_scope":      prop("string", "The place this conversation is happening (e.g. 'project:ghost' when working in that repo). Memories born in the same scope get a boost under a tight budget. A boost, never a filter."),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			Query         string   `json:"query"`
			NS            string   `json:"ns"`
			Kind          string   `json:"kind"`
			Tags          []string `json:"tags"`
			TagMode       string   `json:"tag_mode"`
			Budget        int      `json:"budget"`
			ExcludePinned bool     `json:"exclude_pinned"`
			MinScore      float64  `json:"min_score"`
			ForUser       string   `json:"for_user"`
			ForScope      string   `json:"for_scope"`
			MinSpread     float64  `json:"min_spread"`
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
			TagMode:       p.TagMode,
			Budget:        p.Budget,
			ExcludePinned: p.ExcludePinned,
			MinScore:      p.MinScore,
			MinSpread:     p.MinSpread,

			ForUser: p.ForUser, ForScope: p.ForScope})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_curate",
		Description: "Apply a lifecycle action to a single memory. Use this to directly promote, demote, boost, diminish, archive, delete, pin, or unpin a specific memory by namespace and key. Use op=used to report that an agent actually used a memory (used_count +1, separate from the retrieval-time utility_count).",
		InputSchema: schema([]string{"ns", "key", "op"}, map[string]map[string]any{
			"ns":  prop("string", "Namespace of the memory"),
			"key": prop("string", "Key of the memory"),
			"op":  prop("string", "Action: promote (tier up), demote (tier down), boost (importance +0.2), diminish (importance -0.2), archive (→dormant), delete (soft-delete), pin (always in context), unpin (remove pin), used (agent actually used it: used_count +1)"),
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
		Description: EdgeToolDescription,
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
		Name:        "ghost_patch",
		Description: PatchToolDescription,
		InputSchema: schema([]string{"ns", "key", "patch"}, map[string]map[string]any{
			"ns":           prop("string", "Namespace"),
			"key":          prop("string", "Memory key"),
			"patch":        prop("string", "Unified diff against the memory's current content (as returned by ghost_get)"),
			"base_version": prop("number", "Version the diff was generated against (from ghost_get). Omit or 0 to patch the current head; either way a concurrent move fails with version_conflict rather than overwriting"),
			"dry_run":      prop("boolean", "Validate and return the resulting content without writing"),
			"allow_empty":  prop("boolean", "Permit a patch whose result is empty (refused otherwise — an emptied memory is almost always an upstream failure)"),
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS         string  `json:"ns"`
			Key        string  `json:"key"`
			Patch      string  `json:"patch"`
			BaseVer    float64 `json:"base_version"`
			DryRun     bool    `json:"dry_run"`
			AllowEmpty bool    `json:"allow_empty"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" || p.Key == "" || p.Patch == "" {
			return errResult("ns, key, and patch are required"), nil
		}
		res, err := store.PatchMemory(ctx, st, store.PatchMemoryParams{
			NS: p.NS, Key: p.Key, Patch: p.Patch,
			BaseVersion: int(p.BaseVer), DryRun: p.DryRun, AllowEmpty: p.AllowEmpty,
		})
		if err != nil {
			// Stable machine-readable categories: the agent's repair differs
			// per category, so it must not have to parse prose to choose.
			switch {
			case errors.Is(err, store.ErrVersionConflict):
				return errResult("version_conflict: " + err.Error() + " — re-read with ghost_get, remake the edit against the fresh content, retry"), nil
			case errors.Is(err, store.ErrPatchContext):
				return errResult("context_mismatch: " + err.Error() + " — the stored content differs from what this diff was written against; ghost_get it and regenerate the diff"), nil
			case errors.Is(err, store.ErrPatchMalformed):
				return errResult("malformed_patch: " + err.Error()), nil
			}
			return errResult(err.Error()), nil
		}
		return jsonResult(res)
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
			"ns":           prop("string", "Namespace (e.g. agent:pikamini)"),
			"summary_key":  prop("string", "Key for the new summary memory"),
			"content":      prop("string", "Summary content text (caller must provide — no LLM inside ghost)"),
			"source_keys":  {"type": "array", "items": map[string]any{"type": "string"}, "description": "Keys of memories to consolidate (minimum 2)"},
			"kind":         prop("string", "Memory kind for summary (default: semantic)"),
			"importance":   prop("number", "Importance 0.0-1.0 (default: 0.7)"),
			"source_user":  prop("string", "Person the summary is about, if one; default: derived from the sources (every attributed source names one person → that person)"),
			"source_kind":  prop("string", "stated | observed | self | peer. Default: derived — observed when the sources share one person, else self. A summary never lands with an empty kind"),
			"source_scope": prop("string", "Where the summary was born, e.g. project:ghost; default: carried when all sources share one"),
			"tags":         {"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags for the summary memory"},
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS          string   `json:"ns"`
			SummaryKey  string   `json:"summary_key"`
			Content     string   `json:"content"`
			SourceKeys  []string `json:"source_keys"`
			Kind        string   `json:"kind"`
			Importance  float64  `json:"importance"`
			Tags        []string `json:"tags"`
			SourceUser  string   `json:"source_user"`
			SourceKind  string   `json:"source_kind"`
			SourceScope string   `json:"source_scope"`
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
			SourceUser: p.SourceUser, SourceKind: p.SourceKind, SourceScope: p.SourceScope,
			Tags: p.Tags,
		})
		if err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(result)
	})

	server.AddTool(&mcp.Tool{
		Name:        "ghost_edge_candidates",
		Description: "Return relates_to pairs that do not yet have a typed reasoning edge, so YOU (the calling LLM agent) can classify each one and commit edges via ghost_edge. Ghost itself does zero LLM work — this tool just reads the graph. Workflow: call this → read the returned pairs → for each pair, decide in your own reasoning whether it's caused_by / prevents / implies / none → call ghost_edge(rel=<decision>) for pairs that warrant a reasoning edge. Skip pairs where 'relates_to' (topical only) is already accurate. Pairs that already have a reasoning edge are excluded automatically (safe to re-run).",
		InputSchema: schema([]string{"ns"}, map[string]map[string]any{
			"ns":        prop("string", "Namespace to scan (required)"),
			"max_pairs": prop("integer", "Max candidate pairs to return (default 50). Classification happens in your reasoning — size this for your own context budget, not for cost."),
			"seed":      {"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: only return pairs touching these memory keys. Useful after consolidating a cluster."},
		}),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p struct {
			NS       string   `json:"ns"`
			MaxPairs int      `json:"max_pairs"`
			Seed     []string `json:"seed"`
		}
		if err := unmarshalArgs(req, &p); err != nil {
			return errResult(err.Error()), nil
		}
		if p.NS == "" {
			return errResult("ns is required"), nil
		}
		result, err := st.ListReasoningCandidates(ctx, store.ReasoningCandidatesParams{
			NS:       p.NS,
			MaxPairs: p.MaxPairs,
			Seed:     p.Seed,
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
