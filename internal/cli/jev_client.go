package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// jevClient adapts TypeSafe's Jev decision API to store.InferLLMClient for the
// out-of-band infer-edges command. Jev is not a chat model: it answers typed
// questions. We ask ONE choice question per memory pair — which reasoning
// relation holds — and get a probability per option plus a confidence back.
// That is a better fit for edge classification than free text: the answer is
// structured, and the probability is metadata worth keeping on the edge.
//
// Generate returns the JSON shape parseInferResponse already reads, so the
// store's inference loop needs no change. The reason field carries the
// winning probability and confidence for later review.
const (
	jevURL            = "https://api.typesafe.ai/v1/systemone"
	jevModel          = "jev-latest"
	jevKeyEnv         = "TYPESAFE_API_KEY"
	jevMinProbDefault = 0.6
)

var jevRelations = map[string]string{
	"caused_by": "B is the cause of A (A happened because of B)",
	"prevents":  "B prevents A (B makes A less likely or impossible)",
	"implies":   "A logically implies B, and B is NOT merely a restatement of A (a new conclusion follows)",
	"restates":  "A and B state the same fact or event in different words (paraphrase, subset, or update of the same content)",
	"none":      "No reasoning relationship, or only generic topical similarity",
}

// jevEdgeRels are the answers that become typed edges; the rest are reported
// in the reason but never written. "restates" is deliberately NOT an edge:
// that pair belongs to dedup/consolidation, and surfacing it here is how the
// duplicate glut gets counted for free.
var jevEdgeRels = map[string]bool{"caused_by": true, "prevents": true, "implies": true}

type jevClient struct {
	httpc   *http.Client
	url     string
	key     func() string
	minProb float64
	// Errors are swallowed by the inference loop, so surface the first one
	// once on stderr rather than let a bad key produce a silent zero.
	warnOnce sync.Once
	errs     int
	// counts is the answer distribution over the whole run, printed at exit
	// so a dry run reports what the classifier saw, not only what it wrote.
	counts map[string]int
	// restated pairs are not edges, but they are the dedup backlog the run
	// discovered; keep them so the summary can list them.
	restated []string
}

// keysFromMessage pulls the two memory keys out of the store's pair message.
func keysFromMessage(msg string) (a, b string) {
	const tag = "(key: "
	i := strings.Index(msg, tag)
	if i < 0 {
		return "", ""
	}
	rest := msg[i+len(tag):]
	if j := strings.Index(rest, ")"); j >= 0 {
		a = rest[:j]
		rest = rest[j:]
	}
	if k := strings.Index(rest, tag); k >= 0 {
		rest = rest[k+len(tag):]
		if j := strings.Index(rest, ")"); j >= 0 {
			b = rest[:j]
		}
	}
	return a, b
}

func (j *jevClient) tally(k string) {
	if j.counts == nil {
		j.counts = map[string]int{}
	}
	j.counts[k]++
}

// Summary renders the answer distribution for the run.
func (j *jevClient) Summary() string {
	if len(j.counts) == 0 && j.errs == 0 {
		return ""
	}
	parts := make([]string, 0, len(j.counts)+1)
	for _, k := range []string{"caused_by", "prevents", "implies", "restates", "none", "below-min-prob"} {
		if n := j.counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, n))
		}
	}
	if j.errs > 0 {
		parts = append(parts, fmt.Sprintf("errors=%d", j.errs))
	}
	out := "jev answers: " + strings.Join(parts, " ")
	for _, r := range j.restated {
		out += "\n  restates: " + r
	}
	return out
}

func newJevClient(minProb float64) *jevClient {
	if minProb <= 0 {
		minProb = jevMinProbDefault
	}
	return &jevClient{
		httpc:   &http.Client{Timeout: 15 * time.Second},
		url:     jevURL,
		key:     func() string { return os.Getenv(jevKeyEnv) },
		minProb: minProb,
	}
}

type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type jevAnswer struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Generate satisfies store.InferLLMClient. systemPrompt becomes the question's
// instructions; userMessage (the two memories) becomes the state.
func (j *jevClient) Generate(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	if j.key() == "" {
		return "", errors.New("jev: " + jevKeyEnv + " not set")
	}
	body, err := json.Marshal(map[string]any{
		"state": map[string]string{"memories": userMessage},
		"model": jevModel,
		"questions": map[string]jevQuestion{
			"rel": {Type: "choice", Instructions: systemPrompt, Criteria: jevRelations},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+j.key())
	req.Header.Set("Content-Type", "application/json")

	resp, err := j.httpc.Do(req)
	if err != nil {
		return "", j.fail(fmt.Errorf("jev: %w", err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", j.fail(fmt.Errorf("jev: read: %w", err))
	}
	if resp.StatusCode/100 != 2 {
		// Some APIs echo the credential in an auth error; never let it reach a log.
		msg := strings.ReplaceAll(string(raw), j.key(), "[redacted]")
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return "", j.fail(fmt.Errorf("jev: HTTP %d: %s", resp.StatusCode, msg))
	}
	var out struct {
		Answers map[string]jevAnswer `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", j.fail(fmt.Errorf("jev: decode: %w", err))
	}
	ans, ok := out.Answers["rel"]
	if !ok {
		return "", j.fail(errors.New("jev: no answer for question \"rel\""))
	}
	rel := ans.Choice
	p := ans.Probabilities[rel]
	if !jevEdgeRels[rel] {
		j.tally(rel)
		if rel == "restates" {
			a, b := keysFromMessage(userMessage)
			j.restated = append(j.restated, fmt.Sprintf("%s ~ %s (p=%.2f)", a, b, p))
		}
		return fmt.Sprintf(`{"rel": "none", "reason": "jev chose %s at p=%.2f (not an edge relation)"}`, ans.Choice, p), nil
	}
	if p < j.minProb {
		j.tally("below-min-prob")
		return fmt.Sprintf(`{"rel": "none", "reason": "jev chose %s at p=%.2f, below min-prob %.2f"}`, ans.Choice, p, j.minProb), nil
	}
	j.tally(rel)
	return fmt.Sprintf(`{"rel": "%s", "reason": "jev p=%.2f conf=%.2f"}`, rel, p, ans.Confidence), nil
}

func (j *jevClient) fail(err error) error {
	j.errs++
	j.warnOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "ghost infer-edges: %v (further jev errors are counted, not printed)\n", err)
	})
	return err
}
