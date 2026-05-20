BINARY := ghost
PKG := github.com/rcliao/ghost
MAIN := ./cmd/ghost

.PHONY: build test vet clean install bench-locomo-plus-cache bench-locomo-plus bench-report ort-install ort-build ort-test

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

# ── ORT (onnxruntime) backend for cross-encoder reranker ─────────────
#
# Optional. Enables a CGo-backed cross-encoder ~50× faster than the default
# pure-Go backend, at the cost of (a) two system libraries and (b) a known
# quality regression on cross-encoder rerank — see docs/eval.md.
#
# Setup:
#   make ort-install   # downloads onnxruntime + tokenizers libs
#   make ort-build     # compiles ghost with -tags=ORT
#   make ort-test      # runs LongMemEval (PER_TYPE=20) end-to-end to confirm
#
# Override defaults via env:
#   GHOST_LIBS_DIR=~/.ghost/libs                       # where tokenizers.a lives
#   GHOST_ONNXRUNTIME_PATH=/opt/homebrew/lib           # directory containing libonnxruntime.dylib
#   TOKENIZERS_VERSION=v1.23.0                         # must match hugot's expected version

GHOST_LIBS_DIR        ?= $(HOME)/.ghost/libs
TOKENIZERS_VERSION    ?= v1.23.0

ifeq ($(shell uname -s),Darwin)
  ifeq ($(shell uname -m),arm64)
    TOKENIZERS_ARCH := darwin-arm64
  else
    TOKENIZERS_ARCH := darwin-x86_64
  endif
  GHOST_ONNXRUNTIME_PATH ?= /opt/homebrew/lib
else
  ifeq ($(shell uname -m),aarch64)
    TOKENIZERS_ARCH := linux-arm64
  else
    TOKENIZERS_ARCH := linux-x86_64
  endif
  GHOST_ONNXRUNTIME_PATH ?= /usr/lib/x86_64-linux-gnu
endif

ort-install:
	@mkdir -p $(GHOST_LIBS_DIR)
	@echo "==> Downloading libtokenizers.$(TOKENIZERS_ARCH) $(TOKENIZERS_VERSION)"
	curl -sSL https://github.com/daulet/tokenizers/releases/download/$(TOKENIZERS_VERSION)/libtokenizers.$(TOKENIZERS_ARCH).tar.gz \
	  | tar -xzC $(GHOST_LIBS_DIR)
	@echo "==> libtokenizers.a installed at $(GHOST_LIBS_DIR)/libtokenizers.a"
	@echo "==> Now install onnxruntime separately:"
	@echo "    macOS:   brew install onnxruntime"
	@echo "    Linux:   apt install libonnxruntime-dev (or download from github.com/microsoft/onnxruntime/releases)"
	@echo "==> Then run: make ort-build"

ort-build:
	CGO_LDFLAGS="-L$(GHOST_LIBS_DIR) -L$(GHOST_ONNXRUNTIME_PATH)" \
	CGO_ENABLED=1 \
	  go build -tags=ORT -o $(BINARY)-ort $(MAIN)
	@echo "==> Built ./$(BINARY)-ort with ORT backend"
	@echo "==> Run with: GHOST_ONNXRUNTIME_PATH=$(GHOST_ONNXRUNTIME_PATH) ./$(BINARY)-ort ..."

ort-test:
	CGO_LDFLAGS="-L$(GHOST_LIBS_DIR) -L$(GHOST_ONNXRUNTIME_PATH)" \
	CGO_ENABLED=1 \
	GHOST_ONNXRUNTIME_PATH=$(GHOST_ONNXRUNTIME_PATH) \
	GHOST_EMBED_PROVIDER=local \
	GHOST_RERANKER=local \
	GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
	GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
	GHOST_BENCH_PER_TYPE=20 \
	  go test -tags=ORT ./internal/store/ -run TestLongMemEval$$ -v -timeout 30m
