// The shapes the engine sends. They mirror internal/engine and internal/api:
// the page shows what the engine says and decides nothing itself.

export type Status = 'holds' | 'broken' | 'blocked' | 'unproven' | 'stale'

/** A status as the page shows it: the engine's five, and one more for a fact
 * that held when last checked and whose check did not finish this time. */
export type Shown = Status | 'late'

export interface Evidence {
  status: Status
  detail: string
  /** What a person can run to allow a blocked check. The engine's own field. */
  fix?: string
  at: string
  ms: number
}

export interface Retraction {
  at: string
  by?: string
  reason?: string
}

export interface View {
  id: string
  claim: string
  tags?: string[]
  check?: string
  kind?: string
  by?: string
  created: string
  status: Status
  detail?: string
  /** What a person can run to allow a blocked check. The engine's own field. */
  fix?: string
  checked_at?: string
  ms?: number
  since?: string
  held?: string
  noted_at?: string
  stale_at?: string
  vouched_by?: string
  replaces?: string
  replaced_by?: string
  retracted?: Retraction
  history?: Evidence[]
  fresh?: boolean
  late?: boolean
}

export interface Counts {
  total: number
  holds: number
  broken: number
  blocked: number
  unproven: number
  stale: number
  /** Held when last checked, could not be checked again in time: not under holds. */
  timed_out?: number
  /** Every fact that could not be checked again in time, whatever it was before. */
  late?: number
}

export interface State {
  version: string
  demo: boolean
  now: string
  project: string
  policy: string
  approved: boolean
  facts: View[]
  counts: Counts
  gone?: View[]
  note?: string
}

export interface Verified {
  facts: View[]
  counts: Counts
  note?: string
}

export interface Recalled {
  facts: View[] | null
  matched: number
  known: number
  note?: string
  text: string
}

export interface Parsed {
  ok: boolean
  kind?: string
  check?: string
  error?: string
}

// The demo's pretend machine.

export interface Switch {
  id: string
  group: 'machine' | 'policy'
  do: string
  undo: string
  what: string
  on: boolean
  facts: string[]
}

export interface Note {
  question: string
  query: string
  line: string
  fact: string
}

export interface Machine {
  switches: Switch[]
  notes: Note[]
  written: string
}
