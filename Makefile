BINARY := ghost
PKG := github.com/rcliao/ghost
MAIN := ./cmd/ghost

.PHONY: build test vet clean install bench-locomo-plus-cache bench-locomo-plus bench-report

build:
	go build -o $(BINARY) $(MAIN)

test:
	go test ./... -v

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

install:
	go install $(MAIN)

# ── Benchmark targets ────────────────────────────────────────────────
#
# Prerequisites: testdata/locomo/locomo_plus.json present. Embedding cache
# is built once and reused. LLM runs via `claude -p` (no API key required).
#
# Tunables via env:
#   PER_TYPE=25         → 25 questions per relation type (default = 100q)
#   MODEL=haiku         → Haiku by default; use sonnet for higher quality
#   MODES=...           → comma-separated mode list
#   CHECKPOINT=...      → checkpoint path (default /tmp/ghost-lmplus-bench.json)
#   RESUME=1            → resume from checkpoint if it matches current config

PER_TYPE    ?= 25
MODEL       ?= haiku
MODES       ?= no-memory,ghost,ghost-compress,ghost-compress-wide,oracle
CHECKPOINT  ?= /tmp/ghost-lmplus-bench.json
RESUME      ?= 0

bench-locomo-plus-cache:
	GHOST_EMBED_PROVIDER=local \
	GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
	GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache_plus.json \
	  go test ./internal/store/ -run TestLoCoMoPlusBuildCache -v -timeout 60m

bench-locomo-plus: bench-locomo-plus-cache
	GHOST_EMBED_PROVIDER=local \
	GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
	GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache_plus.json \
	GHOST_BENCH_PER_TYPE=$(PER_TYPE) \
	GHOST_BENCH_LLM_MODEL=$(MODEL) \
	GHOST_BENCH_MODES=$(MODES) \
	GHOST_BENCH_CHECKPOINT=$(CHECKPOINT) \
	GHOST_BENCH_RESUME=$(RESUME) \
	  go test ./internal/store/ -run TestE2ELoCoMoPlus -v -timeout 720m

bench-report:
	@if [ -z "$(CHECKPOINT)" ] || [ ! -f "$(CHECKPOINT)" ]; then \
	  echo "CHECKPOINT=$(CHECKPOINT) not found. Run 'make bench-locomo-plus' first."; exit 1; \
	fi
	go run ./cmd/bench-report -pricing $(MODEL) $(CHECKPOINT)
