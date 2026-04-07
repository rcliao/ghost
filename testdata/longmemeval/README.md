# LongMemEval Dataset

Download the dataset files from HuggingFace before running benchmarks:

```bash
cd testdata/longmemeval/

# Oracle (evidence-only sessions, smallest, fastest)
wget https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_oracle.json

# Small (~115K tokens, ~50 sessions per question)
wget https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json

# Medium (~1.5M tokens, ~500 sessions per question) — very slow
wget https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_m_cleaned.json
```

Then run:

```bash
# Quick check with oracle (evidence-only, fast)
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_oracle.json \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 30m

# Full benchmark with small variant
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 60m

# Limit to N questions (faster iteration)
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_oracle.json \
GHOST_BENCH_LIMIT=20 \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 10m
```

Paper: https://arxiv.org/abs/2410.10813
GitHub: https://github.com/xiaowu0162/LongMemEval
