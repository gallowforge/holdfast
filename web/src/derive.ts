// What the page works out from the engine's answer: order, grouping, the
// one-line summaries. Statuses and evidence are never worked out here; they
// are the engine's to give.
import { age, ago, ms, span } from './format'
import type { Counts, Shown, State, Status, View } from './types'

/** Statuses by how much they need a person, as the engine ranks them. */
export const ORDER: readonly Shown[] = ['broken', 'late', 'blocked', 'stale', 'unproven', 'holds']

/** A fact that held when last checked, and whose check did not finish this time. */
export const timedOut = (v: View): boolean => v.late === true && v.status === 'holds'

/** The status a fact is shown under. A check that did not finish proves nothing, so its fact is not shown as holding. */
export const showAs = (v: View): Shown => (timedOut(v) ? 'late' : v.status)

/** How many facts are shown under a status. */
export const tally = (c: Counts, s: Shown): number => (s === 'late' ? (c.timed_out ?? 0) : c[s])

export const WORD: Record<Shown, string> = {
  holds: 'Holds',
  broken: 'Broken',
  late: 'Timed out',
  blocked: 'Blocked',
  stale: 'Stale',
  unproven: 'Unproven',
}

/** What a status means, in a line a newcomer can read. */
export const MEANS: Record<Shown, string> = {
  holds: 'The check ran and passed.',
  broken: 'The check ran and failed: this was true once and is not now.',
  late: 'The check did not finish in time, so this is as last known and is not proved.',
  blocked: 'The check was not run, so this is neither proved nor disproved.',
  stale: 'A note with no check that has outlived its time to live.',
  unproven: 'A note with no check. Nothing proves it.',
}

/** The engine's own summary line: "13 facts: 1 broken, 1 blocked, 9 hold". */
export function countsLine(c: Counts): string {
  if (c.total === 0) return 'no facts'
  const parts: string[] = []
  const add = (n: number, one: string, many: string) => {
    if (n === 1) parts.push(`1 ${one}`)
    else if (n > 1) parts.push(`${n} ${many}`)
  }
  add(c.broken, 'broken', 'broken')
  add(c.timed_out ?? 0, 'timed out', 'timed out')
  add(c.blocked, 'blocked', 'blocked')
  add(c.stale, 'stale', 'stale')
  add(c.unproven, 'unproven', 'unproven')
  add(c.holds, 'holds', 'hold')
  return `${c.total} ${c.total === 1 ? 'fact' : 'facts'}: ${parts.join(', ')}`
}

/** The sentence at the head of the ledger, and the status it takes its colour from. */
export function headline(c: Counts): { text: string; tone: Shown | 'none' } {
  if (c.total === 0) return { text: 'Nothing is remembered here yet.', tone: 'none' }
  if (c.broken > 0) {
    return { text: c.broken === 1 ? '1 fact is no longer true.' : `${c.broken} facts are no longer true.`, tone: 'broken' }
  }
  const late = c.timed_out ?? 0
  if (late > 0) {
    return { text: late === 1 ? '1 check did not finish in time.' : `${late} checks did not finish in time.`, tone: 'late' }
  }
  const proved = c.holds + c.blocked
  if (proved === 0) return { text: 'Nothing here is proved yet.', tone: 'unproven' }
  if (c.blocked > 0) {
    return { text: c.blocked === 1 ? 'Every check that ran holds. 1 could not run.' : `Every check that ran holds. ${c.blocked} could not run.`, tone: 'blocked' }
  }
  if (c.holds === c.total) return { text: c.total === 1 ? 'The one fact here holds.' : `All ${c.total} facts hold.`, tone: 'holds' }
  return { text: 'Every fact with a check holds.', tone: 'holds' }
}

export interface Group {
  key: 'attention' | 'notes' | 'holds'
  title: string
  hint: string
  facts: View[]
}

/** The ledger in three parts: what needs a person, what nobody has proved, what holds. */
export function groups(facts: readonly View[]): Group[] {
  const rank = (v: View) => ORDER.indexOf(showAs(v))
  const sorted = [...facts].sort((a, b) => rank(a) - rank(b))
  const pick = (...want: Shown[]) => sorted.filter((v) => want.includes(showAs(v)))
  const out: Group[] = [
    { key: 'attention', title: 'Needs a person', hint: 'no longer true, not checked, or past its life', facts: pick('broken', 'late', 'blocked', 'stale') },
    { key: 'notes', title: 'Notes', hint: 'no check, so nothing proves them', facts: pick('unproven') },
    { key: 'holds', title: 'Holds', hint: 'the check ran and passed', facts: pick('holds') },
  ]
  return out.filter((g) => g.facts.length > 0)
}

/** Whether a fact passes the filter box and the status filter. */
export function passes(v: View, query: string, only: Shown | null): boolean {
  if (only && showAs(v) !== only) return false
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return true
  const hay = [v.claim, v.check, v.detail, v.by, v.id, ...(v.tags ?? []).map((t) => '#' + t)].filter(Boolean).join(' ').toLowerCase()
  return words.every((w) => hay.includes(w))
}

/** When a note was last stated or vouched for. */
export const noted = (v: View): number => ms(v.noted_at ?? v.created)

/** How much of its life a note has spent: 0 when new, 1 when it goes stale, more after. */
export function lifeSpent(v: View, now: number): number {
  const from = noted(v)
  const until = ms(v.stale_at)
  if (Number.isNaN(until) || until <= from) return 0
  return Math.max(0, (now - from) / (until - from))
}

/** How dark a note's claim is written: it fades as its life runs out, and stays readable. */
export function ink(v: View, now: number): number {
  if (v.check) return 1
  return 1 - 0.38 * Math.min(1, lifeSpent(v, now))
}

/**
 * A blocked check's reason, and the command that would allow it. The command
 * is the engine's own field and is never looked for inside the text: the text
 * quotes the check, an agent wrote the check, and a command found in there
 * could be one the agent planted. The text is only relieved of its last
 * words when they are exactly the engine's command.
 */
export function splitFix(detail: string | undefined, fix?: string): { why: string; fix?: string } {
  const text = detail ?? ''
  if (!fix) return { why: text }
  const tail = `. A person can allow it: ${fix}`
  return { why: text.endsWith(tail) ? text.slice(0, -tail.length) : text, fix }
}

/** Whether the way to allow a check is a line to paste, or something only to be done by hand. */
export const pasteable = (fix: string | undefined): fix is string => fix?.startsWith('holdfast ') === true

function author(v: View): string {
  let who = 'a note'
  if (v.by) who += ` by ${v.by}`
  if (v.vouched_by && ms(v.noted_at) > ms(v.created)) who += `, confirmed by ${v.vouched_by}`
  return who
}

/** The second line of a row: what the status rests on. */
export function basis(v: View, now: number): string {
  if (timedOut(v)) return 'not proved just now: the check did not finish in time'
  switch (v.status) {
    case 'holds':
      return v.detail ?? ''
    case 'broken':
      return `proof failed: ${v.detail ?? ''}`
    case 'blocked':
      return `not checked: ${splitFix(v.detail, v.fix).why}`
    case 'stale': {
      const life = v.stale_at ? `, past its ${span(ms(v.stale_at) - noted(v))} life` : ''
      return `${author(v)}, ${age(now - noted(v))} old${life}`
    }
    default:
      return `${author(v)}, ${age(now - noted(v))} old`
  }
}

/** The short note at the right of a row. */
export function aside(v: View, now: number): string {
  if (timedOut(v)) return v.checked_at ? `last checked ${ago(now, v.checked_at)}` : 'not proved'
  switch (v.status) {
    case 'holds':
      return v.checked_at ? `checked ${ago(now, v.checked_at)}` : ''
    case 'broken':
      return v.held ? `last held ${ago(now, v.held)}` : 'never held'
    case 'blocked':
      return 'needs a person'
    case 'stale':
      return 'treat it as a guess'
    default:
      return 'nothing proves it'
  }
}

/** The ids whose status is not what it was. */
export function changed(before: readonly View[], after: readonly View[]): string[] {
  const was = new Map(before.map((v) => [v.id, showAs(v)]))
  return after.filter((v) => was.has(v.id) && was.get(v.id) !== showAs(v)).map((v) => v.id)
}

/**
 * The state is the facts as last known: it does not say which checks failed to
 * finish in the last proof. This marks those facts on it again, and takes them
 * out of the count of what holds. A mark lasts for as long as the fact stands on
 * the evidence it stood on then; `late` holds that evidence's time for each id.
 */
export function marked(s: State, late: ReadonlyMap<string, string>): State {
  if (late.size === 0) return s
  let all = 0
  let timed = 0
  const facts = s.facts.map((v) => {
    if (late.get(v.id) !== (v.checked_at ?? '')) return v
    all++
    if (v.status === 'holds') timed++
    return { ...v, late: true }
  })
  if (all === 0) return s
  return { ...s, facts, counts: { ...s.counts, holds: s.counts.holds - timed, timed_out: timed, late: all } }
}

export interface Line {
  /** The status word the line opens with, if it opens with one. */
  label?: string
  tone?: Shown | 'gone'
  text: string
}

// A status only opens a fact when an id follows it: "UNPROVEN and STALE: ..." is advice.
const LABEL = /^(HOLDS|BROKEN|TIMED OUT|BLOCKED|UNPROVEN|STALE|RETRACTED|REPLACED)(\s+)(?=f-)/

/** Splits the text an agent is given into lines, picking out the status each block opens with. */
export function lines(text: string): Line[] {
  let tone: Line['tone']
  return text.split('\n').map((raw) => {
    const m = LABEL.exec(raw)
    if (m) {
      const word = m[1]!.toLowerCase()
      tone = word === 'retracted' || word === 'replaced' ? 'gone' : word === 'timed out' ? 'late' : (word as Status)
      return { label: m[1]! + m[2]!, tone, text: raw.slice(m[0].length) }
    }
    if (!raw.startsWith(' ')) tone = undefined
    return { tone, text: raw }
  })
}
