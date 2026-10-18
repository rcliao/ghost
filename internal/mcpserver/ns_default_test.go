package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rcliao/ghost/internal/store"
)

func mapLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestResolveDefaultNS(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{"neither set", map[string]string{}, "", false},
		{"GHOST_NS only", map[string]string{"GHOST_NS": "agent:umbreon"}, "agent:umbreon", false},
		{"GHOST_AGENT_NS only", map[string]string{"GHOST_AGENT_NS": "agent:cc"}, "agent:cc", false},
		{"GHOST_NS wins", map[string]string{"GHOST_NS": "agent:a", "GHOST_AGENT_NS": "agent:b"}, "agent:a", false},
		{"blank GHOST_NS falls through", map[string]string{"GHOST_NS": "  ", "GHOST_AGENT_NS": "agent:b"}, "agent:b", false},
		{"trimmed", map[string]string{"GHOST_NS": " agent:a \n"}, "agent:a", false},
		{"invalid value errors", map[string]string{"GHOST_NS": "agent a!"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDefaultNS(mapLookup(tc.env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// connect stands up the real tool set over an in-memory transport so the
// test exercises exactly what an MCP client sees.
func connect(t *testing.T, defaultNS string) (*mcp.ClientSession, store.Store) {
	t.Helper()
	t.Setenv("GHOST_EMBED_PROVIDER", "none")
	t.Setenv("GHOST_RERANKER", "none")
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "ghost", Version: "test"}, nil)
	registerTools(server, st, defaultNS)
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, st
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestPutOmittedNSDefaultsFromEnv(t *testing.T) {
	cs, st := connect(t, "agent:umbreon")

	out, isErr := callText(t, cs, "ghost_put", map[string]any{"key": "k1", "content": "hello"})
	if isErr {
		t.Fatalf("put without ns should default, got error: %s", out)
	}
	mems, err := st.Get(context.Background(), store.GetParams{NS: "agent:umbreon", Key: "k1"})
	if err != nil || len(mems) != 1 {
		t.Fatalf("expected memory in default ns, got %v %v", mems, err)
	}

	// An explicit ns still wins over the default.
	if out, isErr := callText(t, cs, "ghost_put", map[string]any{"ns": "agent:other", "key": "k2", "content": "x"}); isErr {
		t.Fatalf("explicit put: %s", out)
	}
	if _, err := st.Get(context.Background(), store.GetParams{NS: "agent:other", Key: "k2"}); err != nil {
		t.Fatalf("explicit ns not honoured: %v", err)
	}

	// Read-side tools default the same way.
	out, isErr = callText(t, cs, "ghost_get", map[string]any{"key": "k1"})
	if isErr || !strings.Contains(out, `"agent:umbreon"`) || !strings.Contains(out, `"hello"`) {
		t.Fatalf("get without ns: isErr=%v out=%s", isErr, out)
	}
}

func TestPutOmittedNSWithoutDefaultErrors(t *testing.T) {
	cs, _ := connect(t, "")
	out, isErr := callText(t, cs, "ghost_put", map[string]any{"key": "k1", "content": "hello"})
	if !isErr {
		t.Fatalf("expected error without ns and without default, got %s", out)
	}
	if !strings.Contains(out, "GHOST_NS") {
		t.Errorf("error should point at GHOST_NS, got %q", out)
	}
}

func TestToolSchemaNSRequiredOnlyWithoutDefault(t *testing.T) {
	defaulted := []string{"ghost_put", "ghost_get", "ghost_patch", "ghost_curate", "ghost_edge",
		"ghost_expand", "ghost_consolidate", "ghost_edge_candidates"}
	for _, tc := range []struct {
		defaultNS  string
		wantNSReqd bool
	}{{"", true}, {"agent:x", false}} {
		cs, _ := connect(t, tc.defaultNS)
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		byName := map[string]*mcp.Tool{}
		for _, tool := range res.Tools {
			byName[tool.Name] = tool
		}
		for _, name := range defaulted {
			tool := byName[name]
			if tool == nil {
				t.Fatalf("tool %s missing", name)
			}
			raw, _ := json.Marshal(tool.InputSchema)
			var sch struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &sch); err != nil {
				t.Fatalf("%s schema: %v", name, err)
			}
			hasNS := false
			for _, r := range sch.Required {
				if r == "ns" {
					hasNS = true
				}
			}
			if hasNS != tc.wantNSReqd {
				t.Errorf("default=%q %s: ns required=%v, want %v", tc.defaultNS, name, hasNS, tc.wantNSReqd)
			}
			if !strings.Contains(sch.Properties["ns"].Description, "GHOST_NS") {
				t.Errorf("%s ns description should mention GHOST_NS: %q", name, sch.Properties["ns"].Description)
			}
		}
	}
}
