package store

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/rcliao/ghost/internal/model"
)

// Compile-time check: MockStore implements Store.
var _ Store = (*MockStore)(nil)

// MockStore is an in-memory implementation of Store for testing.
type MockStore struct {
	mu       sync.RWMutex
	memories map[string]model.Memory // id -> memory
	links    []Link
	files    map[string][]model.FileRef // memory_id -> file refs
	rules    map[string]ReflectRule     // id -> rule
	entropy  *rand.Rand
}

// NewMockStore creates a new in-memory MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		memories: make(map[string]model.Memory),
		files:    make(map[string][]model.FileRef),
		rules:    make(map[string]ReflectRule),
		entropy:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (m *MockStore) newID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), m.entropy).String()
}

func (m *MockStore) Put(_ context.Context, p PutParams) (*model.Memory, error) {
	if err := ValidateNS(p.NS); err != nil {
		return nil, fmt.Errorf("invalid namespace: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	id := m.newID()

	tier := tierOrDefault(p.Tier)

	kind := p.Kind
	if kind == "" {
		switch tier {
		case "sensory", "stm":
			kind = "episodic"
		default:
			kind = "semantic"
		}
	}
	priority := p.Priority
	if priority == "" {
		priority = "normal"
	}

	// Find existing latest version
	version := 1
	var supersedes string
	for _, mem := range m.memories {
		if mem.NS == p.NS && mem.Key == p.Key && mem.DeletedAt == nil {
			if mem.Version >= version {
				version = mem.Version + 1
				supersedes = mem.ID
			}
		}
	}

	var expiresAt *time.Time
	if p.TTL != "" {
		d, err := ParseTTL(p.TTL)
		if err != nil {
			return nil, fmt.Errorf("invalid ttl: %w", err)
		}
		t := now.Add(d)
		expiresAt = &t
	}

	var tags []string
	if len(p.Tags) > 0 {
		tags = make([]string, len(p.Tags))
		copy(tags, p.Tags)
	}

	importance := p.Importance
	if importance <= 0 {
		importance = 0.5
	}

	pinned := p.Pinned
	if p.Tier == "identity" {
		pinned = true
	}

	mem := model.Memory{
		ID:         id,
		NS:         p.NS,
		Key:        p.Key,
		Content:    p.Content,
		Kind:       kind,
		Tags:       tags,
		Version:    version,
		Supersedes: supersedes,
		CreatedAt:  now,
		Priority:   priority,
		Importance: importance,
		Tier:       tier,
		Pinned:     pinned,
		EstTokens:  (len(p.Content) / 4) + 20,
		Meta:       p.Meta,
		ExpiresAt:  expiresAt,
		ChunkCount: 1, // simplified: one chunk per memory
	}

	// Store file refs
	var fileRefs []model.FileRef
	for _, f := range p.Files {
		rel := f.Rel
		if rel == "" {
			rel = "modified"
		}
		fileRefs = append(fileRefs, model.FileRef{Path: f.Path, Rel: rel})
	}
	if len(fileRefs) > 0 {
		m.files[id] = fileRefs
		mem.Files = fileRefs
	}

	m.memories[id] = mem
	return &mem, nil
}

func (m *MockStore) Get(_ context.Context, p GetParams) ([]model.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	var results []model.Memory

	for _, mem := range m.memories {
		if mem.NS != p.NS || mem.Key != p.Key || mem.DeletedAt != nil {
			continue
		}
		if !p.History && mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if p.Version > 0 && mem.Version != p.Version {
			continue
		}
		cp := mem
		if files, ok := m.files[cp.ID]; ok {
			cp.Files = files
		}
		results = append(results, cp)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}

	// Sort by version descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Version > results[j].Version
	})

	if !p.History && p.Version == 0 {
		results = results[:1]
	}

	// Update access tracking for latest
	if !p.History && len(results) > 0 {
		if orig, ok := m.memories[results[0].ID]; ok {
			orig.AccessCount++
			t := now
			orig.LastAccessedAt = &t
			m.memories[results[0].ID] = orig
		}
	}

	return results, nil
}

func (m *MockStore) History(_ context.Context, p HistoryParams) ([]model.Memory, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []model.Memory
	for _, mem := range m.memories {
		if mem.NS != p.NS || mem.Key != p.Key {
			continue
		}
		cp := mem
		if files, ok := m.files[cp.ID]; ok {
			cp.Files = files
		}
		results = append(results, cp)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}

	// Sort by version ascending (chronological)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Version < results[j].Version
	})

	return results, nil
}

func (m *MockStore) List(_ context.Context, p ListParams) ([]model.Memory, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	nsf := ParseNSFilter(p.NS)

	// Find latest version of each ns+key
	latest := map[string]model.Memory{} // "ns:key" -> latest memory
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		if p.Kind != "" && mem.Kind != p.Kind {
			continue
		}
		if len(p.Tags) > 0 && !hasAllTags(mem.Tags, p.Tags) {
			continue
		}
		nk := mem.NS + ":" + mem.Key
		if existing, ok := latest[nk]; !ok || mem.Version > existing.Version {
			latest[nk] = mem
		}
	}

	var results []model.Memory
	for _, mem := range latest {
		results = append(results, mem)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *MockStore) Rm(_ context.Context, p RmParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p.Hard {
		if p.AllVersions {
			for id, mem := range m.memories {
				if mem.NS == p.NS && mem.Key == p.Key {
					delete(m.memories, id)
					delete(m.files, id)
				}
			}
			return nil
		}
		// Hard delete latest only
		latestID := m.latestID(p.NS, p.Key)
		if latestID == "" {
			return fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
		}
		delete(m.memories, latestID)
		delete(m.files, latestID)
		return nil
	}

	now := time.Now().UTC()
	if p.AllVersions {
		for id, mem := range m.memories {
			if mem.NS == p.NS && mem.Key == p.Key && mem.DeletedAt == nil {
				mem.DeletedAt = &now
				m.memories[id] = mem
			}
		}
		return nil
	}

	latestID := m.latestID(p.NS, p.Key)
	if latestID == "" {
		return fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}
	mem := m.memories[latestID]
	mem.DeletedAt = &now
	m.memories[latestID] = mem
	return nil
}

// latestID returns the ID of the latest non-deleted version. Must be called with lock held.
func (m *MockStore) latestID(ns, key string) string {
	var bestID string
	bestVersion := 0
	for _, mem := range m.memories {
		if mem.NS == ns && mem.Key == key && mem.DeletedAt == nil && mem.Version > bestVersion {
			bestVersion = mem.Version
			bestID = mem.ID
		}
	}
	return bestID
}

func (m *MockStore) Search(_ context.Context, p SearchParams) ([]SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	query := strings.ToLower(p.Query)
	nsf := ParseNSFilter(p.NS)

	// Find latest version of each ns+key
	latest := map[string]model.Memory{}
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		if p.Kind != "" && mem.Kind != p.Kind {
			continue
		}
		nk := mem.NS + ":" + mem.Key
		if existing, ok := latest[nk]; !ok || mem.Version > existing.Version {
			latest[nk] = mem
		}
	}

	var results []SearchResult
	for _, mem := range latest {
		if strings.Contains(strings.ToLower(mem.Content), query) ||
			strings.Contains(strings.ToLower(mem.Key), query) {
			results = append(results, SearchResult{Memory: mem})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *MockStore) GC(_ context.Context) (GCResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	var result GCResult

	for id, mem := range m.memories {
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			result.MemoriesDeleted++
			result.ChunksFreed++
			delete(m.memories, id)
			delete(m.files, id)
		}
	}
	return result, nil
}

func (m *MockStore) GCDryRun(_ context.Context) (GCResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	var result GCResult

	for _, mem := range m.memories {
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			result.MemoriesDeleted++
			result.ChunksFreed++
		}
	}
	return result, nil
}

func (m *MockStore) GCStale(_ context.Context, staleThreshold time.Duration) (GCStaleResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-staleThreshold)
	var result GCStaleResult

	for id, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		accessTime := mem.CreatedAt
		if mem.LastAccessedAt != nil {
			accessTime = *mem.LastAccessedAt
		}
		if accessTime.Before(cutoff) {
			if mem.Priority == "high" || mem.Priority == "critical" {
				result.ProtectedCount++
			} else {
				t := now
				mem.DeletedAt = &t
				m.memories[id] = mem
				result.MemoriesDeleted++
			}
		}
	}
	return result, nil
}

func (m *MockStore) GCStaleDryRun(_ context.Context, staleThreshold time.Duration) (GCStaleResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	cutoff := now.Add(-staleThreshold)
	var result GCStaleResult

	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		accessTime := mem.CreatedAt
		if mem.LastAccessedAt != nil {
			accessTime = *mem.LastAccessedAt
		}
		if accessTime.Before(cutoff) {
			if mem.Priority == "high" || mem.Priority == "critical" {
				result.ProtectedCount++
			} else {
				result.MemoriesDeleted++
			}
		}
	}
	return result, nil
}

func (m *MockStore) MemoryCount(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	var count int64
	for _, mem := range m.memories {
		if mem.DeletedAt == nil && (mem.ExpiresAt == nil || mem.ExpiresAt.After(now)) {
			count++
		}
	}
	return count, nil
}

func (m *MockStore) Stats(_ context.Context, dbPath string) (*Stats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	st := &Stats{DBPath: dbPath}

	nsMap := map[string]*NamespaceStats{}
	for _, mem := range m.memories {
		st.TotalMemories++
		st.TotalChunks++ // 1 chunk per memory in mock
		if mem.DeletedAt == nil {
			st.ActiveMemories++
			ns, ok := nsMap[mem.NS]
			if !ok {
				ns = &NamespaceStats{NS: mem.NS}
				nsMap[mem.NS] = ns
			}
			ns.Count++
		}
	}

	// Count distinct keys per namespace
	nsKeys := map[string]map[string]bool{}
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if _, ok := nsKeys[mem.NS]; !ok {
			nsKeys[mem.NS] = map[string]bool{}
		}
		nsKeys[mem.NS][mem.Key] = true
	}
	for ns, keys := range nsKeys {
		if s, ok := nsMap[ns]; ok {
			s.Keys = len(keys)
		}
	}

	for _, ns := range nsMap {
		st.Namespaces = append(st.Namespaces, *ns)
	}
	sort.Slice(st.Namespaces, func(i, j int) bool {
		return st.Namespaces[i].Count > st.Namespaces[j].Count
	})

	return st, nil
}

func (m *MockStore) ListNamespaces(_ context.Context) ([]NamespaceStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nsMap := map[string]*NamespaceStats{}
	nsKeys := map[string]map[string]bool{}

	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		ns, ok := nsMap[mem.NS]
		if !ok {
			ns = &NamespaceStats{NS: mem.NS}
			nsMap[mem.NS] = ns
			nsKeys[mem.NS] = map[string]bool{}
		}
		ns.Count++
		nsKeys[mem.NS][mem.Key] = true
	}
	for ns, keys := range nsKeys {
		nsMap[ns].Keys = len(keys)
	}

	var result []NamespaceStats
	for _, ns := range nsMap {
		result = append(result, *ns)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})
	return result, nil
}

func (m *MockStore) RmNamespace(_ context.Context, ns string, hard bool) (int64, error) {
	nsf := ParseNSFilter(ns)
	if nsf.Pattern == "" && !nsf.IsPrefix {
		return 0, fmt.Errorf("namespace filter cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	var count int64

	if hard {
		for id, mem := range m.memories {
			if nsf.MatchNS(mem.NS) {
				delete(m.memories, id)
				delete(m.files, id)
				count++
			}
		}
	} else {
		for id, mem := range m.memories {
			if nsf.MatchNS(mem.NS) && mem.DeletedAt == nil {
				mem.DeletedAt = &now
				m.memories[id] = mem
				count++
			}
		}
	}
	return count, nil
}

func (m *MockStore) ExportAll(_ context.Context, ns string) ([]model.Memory, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nsf := ParseNSFilter(ns)

	var results []model.Memory
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		results = append(results, mem)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].NS != results[j].NS {
			return results[i].NS < results[j].NS
		}
		if results[i].Key != results[j].Key {
			return results[i].Key < results[j].Key
		}
		return results[i].Version < results[j].Version
	})
	return results, nil
}

func (m *MockStore) Import(ctx context.Context, memories []model.Memory) (int, error) {
	imported := 0
	for _, mem := range memories {
		_, err := m.Put(ctx, PutParams{
			NS:       mem.NS,
			Key:      mem.Key,
			Content:  mem.Content,
			Kind:     mem.Kind,
			Tags:     mem.Tags,
			Priority: mem.Priority,
			Meta:     mem.Meta,
		})
		if err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}

func (m *MockStore) Link(_ context.Context, p LinkParams) (*Link, error) {
	if !validRels[p.Rel] {
		return nil, fmt.Errorf("invalid relation %q (valid: relates_to, contradicts, depends_on, refines)", p.Rel)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	fromID := m.latestID(p.FromNS, p.FromKey)
	if fromID == "" {
		return nil, fmt.Errorf("resolve from: memory not found: %s:%s", p.FromNS, p.FromKey)
	}
	toID := m.latestID(p.ToNS, p.ToKey)
	if toID == "" {
		return nil, fmt.Errorf("resolve to: memory not found: %s:%s", p.ToNS, p.ToKey)
	}

	if p.Remove {
		filtered := m.links[:0]
		for _, l := range m.links {
			if l.FromID == fromID && l.ToID == toID && l.Rel == p.Rel {
				continue
			}
			filtered = append(filtered, l)
		}
		m.links = filtered
		return &Link{FromID: fromID, ToID: toID, Rel: p.Rel}, nil
	}

	// Check for duplicate
	for _, l := range m.links {
		if l.FromID == fromID && l.ToID == toID && l.Rel == p.Rel {
			return &Link{FromID: fromID, ToID: toID, Rel: p.Rel, CreatedAt: l.CreatedAt}, nil
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	link := Link{FromID: fromID, ToID: toID, Rel: p.Rel, CreatedAt: now}
	m.links = append(m.links, link)
	return &link, nil
}

func (m *MockStore) GetLinks(_ context.Context, memoryID string) ([]Link, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Link
	for _, l := range m.links {
		if l.FromID == memoryID || l.ToID == memoryID {
			result = append(result, l)
		}
	}
	return result, nil
}

func (m *MockStore) Context(ctx context.Context, p ContextParams) (*ContextResult, error) {
	budget := p.Budget
	if budget <= 0 {
		budget = 4000
	}
	charBudget := budget * 4

	results, err := m.Search(ctx, SearchParams{
		NS:    p.NS,
		Query: p.Query,
		Kind:  p.Kind,
		Limit: 50,
	})
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return &ContextResult{Budget: budget, Used: 0, Memories: []ContextMemory{}}, nil
	}

	result := &ContextResult{Budget: budget, Memories: []ContextMemory{}}
	used := 0

	for _, r := range results {
		contentLen := len(r.Content)
		if used+contentLen <= charBudget {
			result.Memories = append(result.Memories, ContextMemory{
				NS:      r.NS,
				Key:     r.Key,
				Kind:    r.Kind,
				Content: r.Content,
				Score:   0.5,
			})
			used += contentLen
		} else if remaining := charBudget - used; remaining >= 100 {
			excerpt := r.Content
			if len(excerpt) > remaining {
				excerpt = excerpt[:remaining] + "..."
			}
			result.Memories = append(result.Memories, ContextMemory{
				NS:      r.NS,
				Key:     r.Key,
				Kind:    r.Kind,
				Content: excerpt,
				Score:   0.5,
				Excerpt: true,
			})
			used += len(excerpt)
			break
		} else {
			break
		}
	}

	result.Used = used / 4
	return result, nil
}

func (m *MockStore) GetFiles(_ context.Context, memoryID string) ([]model.FileRef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	files := m.files[memoryID]
	result := make([]model.FileRef, len(files))
	copy(result, files)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func (m *MockStore) FindByFile(_ context.Context, p FindByFileParams) ([]model.Memory, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	// Find memory IDs that reference this file
	matchIDs := map[string]bool{}
	for memID, refs := range m.files {
		for _, ref := range refs {
			if ref.Path == p.Path && (p.Rel == "" || ref.Rel == p.Rel) {
				matchIDs[memID] = true
			}
		}
	}

	// Filter to latest version of each ns+key among matches
	latest := map[string]model.Memory{}
	for _, mem := range m.memories {
		if !matchIDs[mem.ID] {
			continue
		}
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		nk := mem.NS + ":" + mem.Key
		if existing, ok := latest[nk]; !ok || mem.Version > existing.Version {
			cp := mem
			if files, ok := m.files[cp.ID]; ok {
				cp.Files = files
			}
			latest[nk] = cp
		}
	}

	var results []model.Memory
	for _, mem := range latest {
		results = append(results, mem)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *MockStore) ListTags(_ context.Context, ns string) ([]TagInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	nsf := ParseNSFilter(ns)

	// Find latest version of each ns+key
	latest := map[string]model.Memory{}
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		nk := mem.NS + ":" + mem.Key
		if existing, ok := latest[nk]; !ok || mem.Version > existing.Version {
			latest[nk] = mem
		}
	}

	tagCounts := map[string]int{}
	for _, mem := range latest {
		for _, t := range mem.Tags {
			tagCounts[t]++
		}
	}

	var result []TagInfo
	for tag, count := range tagCounts {
		result = append(result, TagInfo{Tag: tag, Count: count})
	}
	return result, nil
}

func (m *MockStore) RenameTag(_ context.Context, oldTag, newTag, ns string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	nsf := ParseNSFilter(ns)
	count := 0

	for id, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		changed := false
		seen := map[string]bool{}
		var newTags []string
		for _, t := range mem.Tags {
			if t == oldTag {
				t = newTag
				changed = true
			}
			if !seen[t] {
				seen[t] = true
				newTags = append(newTags, t)
			}
		}
		if changed {
			mem.Tags = newTags
			m.memories[id] = mem
			count++
		}
	}
	return count, nil
}

func (m *MockStore) RemoveTag(_ context.Context, tag, ns string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	nsf := ParseNSFilter(ns)
	count := 0

	for id, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		var newTags []string
		found := false
		for _, t := range mem.Tags {
			if t == tag {
				found = true
			} else {
				newTags = append(newTags, t)
			}
		}
		if found {
			mem.Tags = newTags
			m.memories[id] = mem
			count++
		}
	}
	return count, nil
}

func (m *MockStore) Peek(_ context.Context, ns string) (*PeekResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	nsf := ParseNSFilter(ns)
	result := &PeekResult{
		NS:             ns,
		MemoryCounts:   map[string]int{},
		TotalEstTokens: map[string]int{},
		RecentTopics:   []string{},
		HighImportance: []MemoryStub{},
	}

	// Collect active memories
	var active []model.Memory
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if mem.ExpiresAt != nil && mem.ExpiresAt.Before(now) {
			continue
		}
		if !nsf.MatchNS(mem.NS) {
			continue
		}
		active = append(active, mem)
	}

	// Tier counts and token totals
	for _, mem := range active {
		tier := mem.Tier
		if tier == "" {
			tier = "stm"
		}
		result.MemoryCounts[tier]++
		result.TotalEstTokens[tier] += mem.EstTokens
	}

	// Pinned summary
	for _, mem := range active {
		if mem.Pinned {
			summary := mem.Content
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}
			result.PinnedSummary = summary
			break
		}
	}

	// Recent topics (top-5 tags by recency)
	sort.Slice(active, func(i, j int) bool {
		return active[i].CreatedAt.After(active[j].CreatedAt)
	})
	tagSeen := map[string]bool{}
	for _, mem := range active {
		for _, t := range mem.Tags {
			if !tagSeen[t] && len(result.RecentTopics) < 5 {
				tagSeen[t] = true
				result.RecentTopics = append(result.RecentTopics, t)
			}
		}
		if len(result.RecentTopics) >= 5 {
			break
		}
	}

	// Top-5 by importance
	sort.Slice(active, func(i, j int) bool {
		if active[i].Importance != active[j].Importance {
			return active[i].Importance > active[j].Importance
		}
		return active[i].CreatedAt.After(active[j].CreatedAt)
	})
	for i, mem := range active {
		if i >= 5 {
			break
		}
		summary := mem.Content
		if len(summary) > 80 {
			summary = summary[:80] + "..."
		}
		result.HighImportance = append(result.HighImportance, MemoryStub{
			ID:         mem.ID,
			Key:        mem.Key,
			Kind:       mem.Kind,
			Tier:       mem.Tier,
			Importance: mem.Importance,
			EstTokens:  mem.EstTokens,
			Summary:    summary,
		})
	}

	return result, nil
}

func (m *MockStore) Curate(ctx context.Context, p CurateParams) (*CurateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := p.NS + ":" + p.Key
	mem, ok := m.memories[key]
	if !ok {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}

	result := &CurateResult{NS: p.NS, Key: p.Key, Op: p.Op}
	switch p.Op {
	case "promote":
		result.OldTier = mem.Tier
		if t, ok := tierUp[mem.Tier]; ok {
			mem.Tier = t
			result.NewTier = t
		}
	case "demote":
		result.OldTier = mem.Tier
		if t, ok := tierDown[mem.Tier]; ok {
			mem.Tier = t
			result.NewTier = t
		}
	case "boost":
		result.OldImportance = mem.Importance
		mem.Importance += 0.2
		if mem.Importance > 1.0 {
			mem.Importance = 1.0
		}
		result.NewImportance = mem.Importance
	case "diminish":
		result.OldImportance = mem.Importance
		mem.Importance -= 0.2
		if mem.Importance < 0.1 {
			mem.Importance = 0.1
		}
		result.NewImportance = mem.Importance
	case "delete":
		now := time.Now()
		mem.DeletedAt = &now
	case "archive":
		result.OldTier = mem.Tier
		mem.Tier = "dormant"
		result.NewTier = "dormant"
	case "pin":
		result.OldPinned = mem.Pinned
		mem.Pinned = true
		result.NewPinned = true
	case "unpin":
		result.OldPinned = mem.Pinned
		mem.Pinned = false
		result.NewPinned = false
	}
	m.memories[key] = mem
	return result, nil
}

func (m *MockStore) Reflect(_ context.Context, p ReflectParams) (*ReflectResult, error) {
	// Simplified mock: just count memories that would be evaluated
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := &ReflectResult{}
	for _, mem := range m.memories {
		if mem.DeletedAt != nil {
			continue
		}
		if p.NS != "" {
			nsf := ParseNSFilter(p.NS)
			if !nsf.MatchNS(mem.NS) {
				continue
			}
		}
		result.MemoriesEvaluated++
	}
	return result, nil
}

func (m *MockStore) RuleSet(_ context.Context, rule ReflectRule) (*ReflectRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if rule.ID == "" {
		rule.ID = m.newID()
	}
	if rule.Name == "" {
		return nil, fmt.Errorf("rule name is required")
	}
	if !validActionOps[rule.Action.Op] {
		return nil, fmt.Errorf("invalid action op %q", rule.Action.Op)
	}
	if rule.Priority == 0 {
		rule.Priority = 50
	}
	if rule.Scope == "" {
		rule.Scope = "reflect"
	}
	if rule.CreatedBy == "" {
		rule.CreatedBy = "user"
	}
	rule.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	m.rules[rule.ID] = rule
	return &rule, nil
}

func (m *MockStore) RuleGet(_ context.Context, id string) (*ReflectRule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	r, ok := m.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found: %s", id)
	}
	return &r, nil
}

func (m *MockStore) RuleList(_ context.Context, ns string) ([]ReflectRule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var rules []ReflectRule
	for _, r := range m.rules {
		if ns != "" && r.NS != "" && r.NS != ns {
			continue
		}
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})
	return rules, nil
}

func (m *MockStore) RuleDelete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.rules[id]; !ok {
		return fmt.Errorf("rule not found: %s", id)
	}
	delete(m.rules, id)
	return nil
}

func (m *MockStore) UtilityInc(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mem, ok := m.memories[id]
	if !ok || mem.DeletedAt != nil {
		return fmt.Errorf("memory not found: %s", id)
	}
	mem.UtilityCount++
	m.memories[id] = mem
	return nil
}

func (m *MockStore) Close() error {
	return nil
}

func (m *MockStore) CreateEdge(_ context.Context, p EdgeParams) (*Edge, error) {
	if !validEdgeRels[p.Rel] {
		return nil, fmt.Errorf("invalid relation %q", p.Rel)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	fromID := m.latestID(p.FromNS, p.FromKey)
	if fromID == "" {
		return nil, fmt.Errorf("resolve from: memory not found: %s:%s", p.FromNS, p.FromKey)
	}
	toID := m.latestID(p.ToNS, p.ToKey)
	if toID == "" {
		return nil, fmt.Errorf("resolve to: memory not found: %s:%s", p.ToNS, p.ToKey)
	}

	weight := p.Weight
	if weight <= 0 {
		weight = defaultEdgeWeight(p.Rel)
	}
	now := time.Now().UTC()
	edge := Edge{FromID: fromID, ToID: toID, Rel: p.Rel, Weight: weight, CreatedAt: now}
	return &edge, nil
}

func (m *MockStore) DeleteEdge(_ context.Context, p EdgeParams) error {
	return nil
}

func (m *MockStore) GetEdges(_ context.Context, memoryID string) ([]Edge, error) {
	return nil, nil
}

func (m *MockStore) GetEdgesByNSKey(_ context.Context, ns, key string) ([]Edge, error) {
	return nil, nil
}

// hasAllTags returns true if memTags contains all of the required tags.
func hasAllTags(memTags, required []string) bool {
	set := map[string]bool{}
	for _, t := range memTags {
		set[t] = true
	}
	for _, t := range required {
		if !set[t] {
			return false
		}
	}
	return true
}
