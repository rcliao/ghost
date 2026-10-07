// ghost-eval: ghost as a coding agent meets it in daily use, inside an isolated
// `claude plugin eval` run (which loads no personal hooks or MCP servers).
// - per-prompt injection with the production prompt hook's settings
// - an explicit recall tool (the ghost_context channel)
// - the ghost-lens system-prompt nudge
// Memories come from fixture/memory.db (built by build-fixture.sh), copied per
// session so access-count writes never leak between runs.
import type { EngineInterface, Register } from 'claude-code'

const NS = 'eval:ghost'
const TOOL = 'mcp__ghost-eval__recall'
// Mirrors ~/.claude/hooks/ghost-user-prompt.sh (project+general budgets summed; one call here).
const BUDGET = 1500
const MIN_SCORE = '0.55'
const MIN_SPREAD = '0.15'
const MIN_WORDS = 5
const NUDGE =
  `Before non-trivial work in an unfamiliar area, a design decision, or debugging an error you may have seen before, call ${TOOL} with the task as the query. Automatically injected ghost memories are often off-topic: ignore any that do not apply.`

let ghost = 'ghost'
let db: string | undefined
// A/B variant: extra environment for every ghost call (e.g. GHOST_EDGE_*), set by
// set-variant.sh into fixture/config.json. Eval runs may not inherit the caller's env.
let variant = 'baseline'
let ghostEnv: Record<string, string> = {}
let injected = new Set<string>()

type Memory = { key: string; content: string; score?: number }

async function context($: EngineInterface, query: string, budget: number, floor: boolean): Promise<Memory[]> {
  if (db === undefined) return []
  const argv = [ghost, '--db', db, 'context', query, '-n', NS, '--budget', String(budget)]
  if (floor) argv.push('--min-score', MIN_SCORE, '--min-spread', MIN_SPREAD)
  try {
    const ran = await $.process.run(argv, { timeoutMs: 180_000, env: ghostEnv })
    if (ran.exitCode !== 0) return []
    const parsed = JSON.parse(ran.stdout) as { memories?: Memory[] }
    return parsed.memories ?? []
  } catch {
    return []
  }
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const root = $.plugin.root
    try {
      const cfg = JSON.parse(await $.fs.read(`${root}/fixture/config.json`)) as { ghost?: string; variant?: string; env?: Record<string, string> }
      if (typeof cfg.ghost === 'string') ghost = cfg.ghost
      if (typeof cfg.variant === 'string') variant = cfg.variant
      if (cfg.env !== undefined && typeof cfg.env === 'object') ghostEnv = cfg.env
    } catch {
      // ghost on PATH
    }
    const tmp = (await $.env.get('TMPDIR')) ?? '/tmp'
    const copy = `${tmp}/ghost-eval-${await $.session.id()}.db`
    const ran = await $.process.run(['cp', `${root}/fixture/memory.db`, copy]).catch(() => undefined)
    db = ran?.exitCode === 0 ? copy : undefined
    injected = new Set()
    await $.tool.register({
      name: 'recall',
      description: "Search the team's persistent memory (conventions, past decisions, earlier debugging fixes, user preferences) for the task at hand. Returns the most relevant memories.",
      inputSchema: { type: 'object', properties: { query: { type: 'string', description: 'What you are working on' } }, required: ['query'] },
    })
    return next(e)
  })

  on('tool.call', { tool: TOOL }, async ($, e) => {
    const query = (e as unknown as { query?: unknown }).query
    const memories = await context($, typeof query === 'string' ? query : '', 2000, false)
    const text = memories.length === 0 ? 'No memories found.' : memories.map(m => `[${m.key}] ${m.content}`).join('\n')
    return { result: text } as never
  })

  // Production hook behaviour: skip short prompts and harness turns, dedup keys already injected.
  on('prompt.submit', async ($, e, next) => {
    if (e.turnId !== undefined || e.text.startsWith('<') || e.text.trim().split(/\s+/).length <= MIN_WORDS) return next(e)
    const fresh = (await context($, e.text, BUDGET, true)).filter(m => !injected.has(m.key))
    if (fresh.length === 0) return next(e)
    for (const m of fresh) injected.add(m.key)
    // Indicator for the with-only `injection-fired` grader: proves the arm really had ghost.
    try {
      await $.process.run(['mkdir', '-p', '.ghost-eval'])
      await $.fs.write('.ghost-eval/injected.log', `variant=${variant}\n` + [...injected].join('\n') + '\n')
    } catch {
      // an indicator only
    }
    const block = `[Ghost Memory — Relevant]\n${fresh.map(m => `[${m.key}] ${m.content}`).join('\n')}\n[End Ghost Memory]`
    return next({ ...e, context: [...(e.context ?? []), block] })
  })

  on('prompt.compose', async ($, e, next) => {
    const composed = await next(e)
    if (!e.tools.includes(TOOL)) return composed
    return { sections: [...composed.sections, { id: 'ghost-eval:recall', text: NUDGE, scope: 'session' }] }
  })
}
