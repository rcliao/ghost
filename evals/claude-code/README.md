# ghost in Claude Code: agent-level eval

Does ghost make a coding agent better, worse, or just more expensive?
`ghost eval` scores retrieval. This suite scores what the agent does with it.

`ghost-eval/` is a Claude Code plugin that gives an isolated `claude plugin eval` run the ghost a person has in daily use:
- per-prompt injection with the production prompt hook's settings (budget 1500, `--min-score 0.55`, `--min-spread 0.15`, skip prompts of 5 words or fewer)
- an explicit `recall` tool (the `ghost_context` channel)
- the ghost-lens system-prompt nudge

Eval runs load no personal hooks or MCP servers, so the plugin calls `ghost --db fixture/memory.db` itself.
Each session works on its own copy of the fixture.
`--ablation with-without` runs every case again without the plugin, which is the no-ghost baseline.

## Cases

| Case | Kind | What only ghost knows |
|---|---|---|
| api-changes-log | must-have | route changes need a `docs/api-changes.md` entry |
| staging-deploy | must-have | `make deploy ENV=stg REGION=usw2` |
| script-language | must-have | the user prefers JS (.mjs) over Python |
| retry-decision | past decision (injected at 0.40, so this tests injection, not the recall tool) | 3 attempts, 200ms doubling backoff, 5xx and network only |
| deploy-short | recall only | the same deploy fact, but a 5-word prompt skips injection, so only the recall tool can find it |
| stale-config | harm check | stale memories name `settings.yaml` and an extra `deploy/k8s/service.yaml` edit; neither file exists |
| irrelevant-docstring | harm check | retrieval injects an unrelated convention into a one-line task (no guard phrase) |

The fixture also holds:
- ghost's 58 synthetic eval memories as distractors
- four competing memories that are true for sibling repos (acme-billing, acme-web, acme-tools) and wrong for acme-api

The plugin writes `.ghost-eval/injected.log` when it injects. The unscored with-only `injection-fired` grader uses it to prove the with-arm really had ghost.
Cases use Read, Glob, Grep, Edit and Write but not Bash, because a Bash grant refuses to start on a Mac whose `~/.docker/cli-plugins` holds Docker Desktop symlinks.

## Run

```sh
./ghost-eval/build-fixture.sh          # rebuild the fixture: same start for every run
claude plugin eval ./ghost-eval --runs 3 --ablation with-without --scaffold -j 4 \
  --allow-tools Write Edit mcp__ghost-eval__recall --max-cost-usd 3 --trust-plugin --json out.json
```

`--scaffold` runs each case's `fixture.sh` (a small Go repo) as you.
Debug one case cheaply with `--case <name> --runs 1 --ablation none --keep-temp`.
Injected context does not appear as text in the kept `out/trace.jsonl`; judge it from the answer.

## A/B variants

`ghost-eval/set-variant.sh` sets environment for every ghost call the plugin makes, by writing `fixture/config.json`.
The injection indicator (`.ghost-eval/injected.log`) records which variant a run used.

```sh
./ghost-eval/build-fixture.sh
GHOST_BIN=/path/to/ghost ./ghost-eval/set-variant.sh edge-min 0.05   # or baseline | edges-off
claude plugin eval ./ghost-eval --runs 3 --ablation none --scaffold -j 2 ...
```

Run variants from bash, not zsh: zsh doesn't split unquoted variables, so a two-word variant reaches the script as one argument.
Keep concurrency at 2 and run nothing heavy alongside, and check `injection-fired` before trusting a difference.

Eval runs get a fresh HOME. Without `GHOST_MODELS_DIR`, every ghost call downloaded the ~184 MB embedding model, taking 5–10 s. About 8% of runs then lost their injection silently, so the arm quietly became the baseline. `build-fixture.sh` now points the plugin at this machine's `~/.ghost/models`: calls take about 1 s and injection fired in 22 of 22 runs.
