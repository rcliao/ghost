package capture

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMakeKeyCJKStaysValidUTF8(t *testing.T) {
	// 30 CJK runes = 90 bytes; a byte cut at 60 lands mid-rune for some
	// offsets, so try a few leading ASCII widths to hit every alignment.
	cjk := strings.Repeat("我們家週末去好市多買牛奶", 3)
	for pad := 0; pad < 3; pad++ {
		seg := strings.Repeat("a", pad) + cjk
		key := makeKey(seg, nil)
		if !utf8.ValidString(key) {
			t.Errorf("pad=%d: key is invalid UTF-8: %q", pad, key)
		}
		if len(key) > maxKeyBytes {
			t.Errorf("pad=%d: key is %d bytes, want <= %d", pad, len(key), maxKeyBytes)
		}
		if key == "" || key == "note" {
			t.Errorf("pad=%d: expected a CJK-derived key, got %q", pad, key)
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := "ab我們" // 2 + 3 + 3 bytes
	for n, want := range map[int]string{0: "", 2: "ab", 3: "ab", 4: "ab", 5: "ab我", 7: "ab我", 8: "ab我們", 20: "ab我們"} {
		if got := truncateUTF8(s, n); got != want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", s, n, got, want)
		}
	}
}

func TestExtractLongCJKSegmentValidUTF8(t *testing.T) {
	long := "我比較喜歡" + strings.Repeat("在週末早上去公園散步然後喝咖啡", 40)
	cands := Extract(long, Options{MinSalience: 0.01})
	if len(cands) == 0 {
		t.Fatal("expected the preference cue to yield a candidate")
	}
	for _, c := range cands {
		if len(c.Content) > maxSegmentLen {
			t.Errorf("content not truncated: %d bytes", len(c.Content))
		}
		if !utf8.ValidString(c.Content) || !utf8.ValidString(c.Key) {
			t.Errorf("invalid UTF-8 in candidate: key=%q", c.Key)
		}
	}
}

func TestDedupeKeyDistinct(t *testing.T) {
	seen := map[string]bool{}
	for n := 1; n <= 60; n++ {
		k := DedupeKey("notion", n)
		if seen[k] {
			t.Fatalf("DedupeKey repeated %q at n=%d", k, n)
		}
		seen[k] = true
	}
	if DedupeKey("k", 1) != "k-a" || DedupeKey("k", 26) != "k-z" || DedupeKey("k", 27) != "k-27" {
		t.Errorf("unexpected suffix scheme: %q %q %q", DedupeKey("k", 1), DedupeKey("k", 26), DedupeKey("k", 27))
	}
}

func TestSameContent(t *testing.T) {
	if !SameContent("  Tabs over   spaces\n", "tabs over spaces") {
		t.Error("case/whitespace differences should compare equal")
	}
	if SameContent("tabs over spaces", "spaces over tabs") {
		t.Error("different text should not compare equal")
	}
}
