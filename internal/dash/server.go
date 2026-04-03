// Package dash provides an HTTP server for the ghost memory dashboard.
package dash

import (
	"embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rcliao/ghost/internal/store"
)

//go:embed index.html
var staticFS embed.FS

// Server serves the ghost dashboard and API endpoints.
type Server struct {
	store  store.Store
	dbPath string
	mux    *http.ServeMux
}

// New creates a new dashboard server.
func New(s store.Store, dbPath string) *Server {
	srv := &Server{store: s, dbPath: dbPath, mux: http.NewServeMux()}
	srv.mux.HandleFunc("/", srv.handleIndex)
	srv.mux.HandleFunc("/api/stats", srv.handleStats)
	srv.mux.HandleFunc("/api/memories", srv.handleMemories)
	srv.mux.HandleFunc("/api/memory", srv.handleMemory)
	srv.mux.HandleFunc("/api/namespaces", srv.handleNamespaces)
	srv.mux.HandleFunc("/api/clusters", srv.handleClusters)
	srv.mux.HandleFunc("/api/edges", srv.handleEdges)
	srv.mux.HandleFunc("/api/context", srv.handleContext)
	srv.mux.HandleFunc("/api/search", srv.handleSearch)
	return srv
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := staticFS.ReadFile("index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stats, err := s.store.Stats(ctx, s.dbPath)
	if err != nil {
		writeErr(w, err)
		return
	}

	nsList, err := s.store.ListNamespaces(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}

	tierCounts := map[string]int{}
	tierTokens := map[string]int{}
	pinnedCount := 0
	totalTokens := 0

	for _, ns := range nsList {
		mems, err := s.store.List(ctx, store.ListParams{NS: ns.NS, Limit: 10000})
		if err != nil {
			continue
		}
		for _, m := range mems {
			tier := m.Tier
			if tier == "" {
				tier = "stm"
			}
			tierCounts[tier]++
			tierTokens[tier] += m.EstTokens
			totalTokens += m.EstTokens
			if m.Pinned {
				pinnedCount++
			}
		}
	}

	writeJSON(w, map[string]interface{}{
		"total_memories": stats.TotalMemories,
		"total_tokens":   totalTokens,
		"db_size_bytes":  stats.DBSizeBytes,
		"pinned_count":   pinnedCount,
		"tier_counts":    tierCounts,
		"tier_tokens":    tierTokens,
		"namespaces":     nsList,
	})
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	nsList, err := s.store.ListNamespaces(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, nsList)
}

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.URL.Query().Get("ns")
	tier := r.URL.Query().Get("tier")
	kind := r.URL.Query().Get("kind")
	q := r.URL.Query().Get("q")
	limitStr := r.URL.Query().Get("limit")

	limit := 200
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	if q != "" {
		results, err := s.store.Search(ctx, store.SearchParams{
			NS:    ns,
			Query: q,
			Kind:  kind,
			Limit: limit,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		if tier != "" {
			var filtered []store.SearchResult
			for _, sr := range results {
				if sr.Memory.Tier == tier {
					filtered = append(filtered, sr)
				}
			}
			writeJSON(w, filtered)
			return
		}
		writeJSON(w, results)
		return
	}

	var tags []string
	if t := r.URL.Query().Get("tag"); t != "" {
		tags = strings.Split(t, ",")
	}

	mems, err := s.store.List(ctx, store.ListParams{
		NS:    ns,
		Kind:  kind,
		Tags:  tags,
		Limit: limit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	if tier != "" {
		var filtered []interface{}
		for _, m := range mems {
			if m.Tier == tier {
				filtered = append(filtered, m)
			}
		}
		writeJSON(w, filtered)
		return
	}

	writeJSON(w, mems)
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.URL.Query().Get("ns")
	key := r.URL.Query().Get("key")

	if ns == "" || key == "" {
		http.Error(w, "ns and key required", http.StatusBadRequest)
		return
	}

	mems, err := s.store.Get(ctx, store.GetParams{NS: ns, Key: key, History: true})
	if err != nil {
		writeErr(w, err)
		return
	}

	var edges []store.Edge
	if len(mems) > 0 {
		edges, _ = s.store.GetEdges(ctx, mems[0].ID)
	}

	writeJSON(w, map[string]interface{}{
		"memory": mems,
		"edges":  edges,
	})
}

func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		ns = "agent:claude-code"
	}

	clusters, err := s.store.GetSimilarClusters(r.Context(), ns)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, clusters)
}

func (s *Server) handleEdges(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		ns = "agent:claude-code"
	}

	ctx := r.Context()
	mems, err := s.store.List(ctx, store.ListParams{NS: ns, Limit: 10000})
	if err != nil {
		writeErr(w, err)
		return
	}

	type edgeInfo struct {
		FromKey string  `json:"from_key"`
		ToKey   string  `json:"to_key"`
		Rel     string  `json:"rel"`
		Weight  float64 `json:"weight"`
	}

	idToKey := map[string]string{}
	var nodes []map[string]interface{}
	for _, m := range mems {
		idToKey[m.ID] = m.Key
		nodes = append(nodes, map[string]interface{}{
			"key":        m.Key,
			"kind":       m.Kind,
			"tier":       m.Tier,
			"importance": m.Importance,
			"est_tokens": m.EstTokens,
			"pinned":     m.Pinned,
		})
	}

	var edges []edgeInfo
	seen := map[string]bool{}
	for _, m := range mems {
		memEdges, err := s.store.GetEdges(ctx, m.ID)
		if err != nil {
			continue
		}
		for _, e := range memEdges {
			edgeKey := e.FromID + "->" + e.ToID
			if seen[edgeKey] {
				continue
			}
			seen[edgeKey] = true
			fromKey := idToKey[e.FromID]
			toKey := idToKey[e.ToID]
			if fromKey != "" && toKey != "" {
				edges = append(edges, edgeInfo{
					FromKey: fromKey,
					ToKey:   toKey,
					Rel:     e.Rel,
					Weight:  e.Weight,
				})
			}
		}
	}

	writeJSON(w, map[string]interface{}{
		"nodes": nodes,
		"edges": edges,
	})
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q (query) required", http.StatusBadRequest)
		return
	}
	ns := r.URL.Query().Get("ns")
	kind := r.URL.Query().Get("kind")
	budgetStr := r.URL.Query().Get("budget")
	maxMemTokStr := r.URL.Query().Get("max_memory_tokens")
	excludePinned := r.URL.Query().Get("exclude_pinned") == "true"

	budget := 4000
	if budgetStr != "" {
		if v, err := strconv.Atoi(budgetStr); err == nil && v > 0 {
			budget = v
		}
	}
	maxMemTok := 0
	if maxMemTokStr != "" {
		if v, err := strconv.Atoi(maxMemTokStr); err == nil {
			maxMemTok = v
		}
	}

	var tags []string
	if t := r.URL.Query().Get("tags"); t != "" {
		tags = strings.Split(t, ",")
	}

	result, err := s.store.Context(r.Context(), store.ContextParams{
		NS:              ns,
		Query:           q,
		Kind:            kind,
		Tags:            tags,
		Budget:          budget,
		ExcludePinned:   excludePinned,
		MaxMemoryTokens: maxMemTok,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q (query) required", http.StatusBadRequest)
		return
	}
	ns := r.URL.Query().Get("ns")
	kind := r.URL.Query().Get("kind")
	limitStr := r.URL.Query().Get("limit")
	includeAll := r.URL.Query().Get("include_all") == "true"

	limit := 20
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	var tags []string
	if t := r.URL.Query().Get("tags"); t != "" {
		tags = strings.Split(t, ",")
	}

	var excludeTiers []string
	if et := r.URL.Query().Get("exclude_tiers"); et != "" {
		excludeTiers = strings.Split(et, ",")
	}

	results, err := s.store.Search(r.Context(), store.SearchParams{
		NS:           ns,
		Query:        q,
		Kind:         kind,
		Tags:         tags,
		Limit:        limit,
		ExcludeTiers: excludeTiers,
		IncludeAll:   includeAll,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, results)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
