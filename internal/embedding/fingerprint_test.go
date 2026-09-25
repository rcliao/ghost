package embedding

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestEmbedFingerprint prints a stable fingerprint of the local embedder's
// output for fixed inputs, so a runtime upgrade (hugot, ONNX backend, model
// file) can be checked for numeric drift before it lands: run before, run
// after, diff the lines. Stored vectors are only comparable to new ones if
// this does not move. Gated; a measurement, not a pass/fail test.
//
//	GHOST_EMBED_FINGERPRINT=1 GHOST_EMBED_MODEL_LOCAL=paraphrase-multilingual-MiniLM-L12-v2 \
//	  go test ./internal/embedding/ -run TestEmbedFingerprint -v
func TestEmbedFingerprint(t *testing.T) {
	if os.Getenv("GHOST_EMBED_FINGERPRINT") == "" {
		t.Skip("GHOST_EMBED_FINGERPRINT not set")
	}
	os.Setenv("GHOST_EMBED_PROVIDER", "local")
	e := NewFromEnv()
	if e == nil {
		t.Fatal("no embedder")
	}
	inputs := []string{
		"Mami decided to stop drinking kefir; probiotics come from the Floravita capsule now.",
		"優勝美地 9/8 早上 08:00 起床早餐，09:00 退房出發。",
		"deploy pipeline decision: ship on Tuesday, never on Friday afternoon",
	}
	for _, in := range inputs {
		v, err := e.Embed(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		var sum, norm float64
		for _, x := range v {
			sum += float64(x)
			norm += float64(x) * float64(x)
		}
		head := make([]string, 0, 6)
		for i := 0; i < 6 && i < len(v); i++ {
			head = append(head, fmt.Sprintf("%.6f", v[i]))
		}
		t.Logf("dims=%d sum=%.6f norm=%.6f head=[%s]  %q", len(v), sum, norm, strings.Join(head, " "), string([]rune(in)[:min(30, len([]rune(in)))]))
	}
}
