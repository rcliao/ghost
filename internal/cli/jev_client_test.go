package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeJevTransport answers in-process; the sandbox forbids listening sockets,
// and a RoundTripper stub is the tighter test anyway.
type fakeJevTransport struct {
	t      *testing.T
	answer map[string]any
	status int
	seen   []map[string]any
}

func (f *fakeJevTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
		f.t.Errorf("auth header = %q", got)
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode request: %v", err)
	}
	f.seen = append(f.seen, req)
	body, _ := json.Marshal(map[string]any{
		"model":   "jev-latest",
		"answers": map[string]any{"rel": f.answer},
		"usage":   map[string]int{"input_tokens": 10, "output_tokens": 1},
	})
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
}

func newFakeJev(t *testing.T, answer map[string]any, status int) (*jevClient, *[]map[string]any) {
	t.Helper()
	ft := &fakeJevTransport{t: t, answer: answer, status: status}
	c := newJevClient(0.6)
	c.httpc = &http.Client{Transport: ft}
	c.key = func() string { return "test-key" }
	return c, &ft.seen
}

func TestJevClientAsksOneChoiceQuestion(t *testing.T) {
	c, seen := newFakeJev(t, map[string]any{
		"type": "choice", "choice": "caused_by",
		"probabilities": map[string]float64{"caused_by": 0.83, "none": 0.1, "prevents": 0.05, "implies": 0.02},
		"confidence":    0.9,
	}, 200)

	out, err := c.Generate(context.Background(), "SYSTEM", "Memory A ... Memory B ...")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"rel": "caused_by"`) || !strings.Contains(out, "p=0.83") {
		t.Errorf("unexpected output: %s", out)
	}

	req := (*seen)[0]
	if req["model"] != "jev-latest" {
		t.Errorf("model = %v", req["model"])
	}
	qs := req["questions"].(map[string]any)
	q := qs["rel"].(map[string]any)
	if q["type"] != "choice" || q["instructions"] != "SYSTEM" {
		t.Errorf("question = %v", q)
	}
	if crit := q["criteria"].(map[string]any); len(crit) != 5 {
		t.Errorf("criteria = %v", crit)
	}
	if st := req["state"].(map[string]any); st["memories"] != "Memory A ... Memory B ..." {
		t.Errorf("state = %v", st)
	}
}

func TestJevClientLowProbabilityBecomesNone(t *testing.T) {
	c, _ := newFakeJev(t, map[string]any{
		"choice": "implies", "probabilities": map[string]float64{"implies": 0.41, "none": 0.39}, "confidence": 0.5,
	}, 200)
	out, err := c.Generate(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"rel": "none"`) || !strings.Contains(out, "below min-prob") {
		t.Errorf("expected none with reason, got %s", out)
	}
}

func TestJevClientUnknownChoiceIsNone(t *testing.T) {
	c, _ := newFakeJev(t, map[string]any{"choice": "banana", "probabilities": map[string]float64{"banana": 0.99}}, 200)
	out, err := c.Generate(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"rel": "none"`) {
		t.Errorf("got %s", out)
	}
}

func TestJevClientErrorsCountedAndRedacted(t *testing.T) {
	c, _ := newFakeJev(t, nil, 401)
	c.key = func() string { return "test-key" }
	if _, err := c.Generate(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error on 401")
	} else if strings.Contains(err.Error(), "test-key") {
		t.Errorf("credential leaked into error: %v", err)
	}
	if c.errs != 1 {
		t.Errorf("errs = %d", c.errs)
	}
	if _, err := c.Generate(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error")
	}
	if c.errs != 2 {
		t.Errorf("errs = %d", c.errs)
	}
}

func TestJevClientRestatesIsReportedNotWritten(t *testing.T) {
	c, _ := newFakeJev(t, map[string]any{"choice": "restates", "probabilities": map[string]float64{"restates": 0.97}}, 200)
	out, err := c.Generate(context.Background(), "s", "Memory A (key: k1):\nx\n\nMemory B (key: k2):\ny")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Summary(), "restates: k1 ~ k2 (p=0.97)") {
		t.Errorf("summary = %q", c.Summary())
	}
	if !strings.Contains(out, `"rel": "none"`) || !strings.Contains(out, "restates") {
		t.Errorf("got %s", out)
	}
	if !strings.Contains(c.Summary(), "restates=1") {
		t.Errorf("summary = %q", c.Summary())
	}
}

func TestJevClientRequiresKey(t *testing.T) {
	c := newJevClient(0)
	c.key = func() string { return "" }
	if _, err := c.Generate(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected missing-key error")
	}
}
