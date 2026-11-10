// The page's one source of truth: what the engine last said, kept current.
// Every change goes through the engine and comes back as a new state; nothing
// here decides whether a fact holds.
import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { call, ready, Refusal } from './api'
import { changed, marked } from './derive'
import { ms } from './format'
import type { Machine, Parsed, Recalled, State, Verified, View } from './types'

export { Refusal }

export interface Draft {
  claim: string
  check: string
  tags: string[]
  replaces?: string
  ttl?: string
}

export interface Ledger {
  state: State | null
  /** The demo's pretend machine; null in the real tool. */
  machine: Machine | null
  /** The engine's clock, ticking. */
  now: number
  /** When this page last proved everything. */
  provedAt: number | null
  proving: boolean
  /** The switch being flipped, in the demo. */
  flipping: string | null
  /** The switch flipped last, and how many flips there have been: the question to ask follows it. */
  led: { id: string | null; n: number }
  /** Facts whose status has just changed. */
  moved: ReadonlySet<string>
  /** Goes up with every full proof, to reveal the rows one by one. */
  sweep: number
  /** Goes up whenever what an agent would be told may have changed. */
  rev: number
  /** Why the engine cannot be reached, when it cannot. */
  down: string | null
  refresh(): Promise<void>
  proveAll(): Promise<void>
  prove(id: string): Promise<void>
  remember(d: Draft): Promise<{ fact: View; already: boolean }>
  retract(id: string, reason: string): Promise<void>
  vouch(id: string): Promise<void>
  flip(id: string): Promise<void>
  reset(): Promise<void>
}

/** Checks a check as it is typed. */
export const parse = (check: string) => call<Parsed>('parse', { check })

/** Asks what an agent would be told. */
export const recall = (query: string, limit = 0) => call<Recalled>('recall', { query, limit })

const POLL = 5_000
const AWAY = 60_000
const FLASH = 1_600

export function useLedger(): Ledger {
  const [state, setState] = useState<State | null>(null)
  const [machine, setMachine] = useState<Machine | null>(null)
  const [clock, setClock] = useState(() => Date.now())
  const [provedAt, setProvedAt] = useState<number | null>(null)
  const [proving, setProving] = useState(false)
  const [flipping, setFlipping] = useState<string | null>(null)
  const [led, setLed] = useState<{ id: string | null; n: number }>({ id: null, n: 0 })
  const [moved, setMoved] = useState<ReadonlySet<string>>(() => new Set())
  const [sweep, setSweep] = useState(0)
  const [rev, setRev] = useState(0)
  const [down, setDown] = useState<string | null>(null)

  const offset = useRef(0)
  const before = useRef<View[]>([])
  const shape = useRef('')
  const asked = useRef(0)
  const taken = useRef(0)
  const busy = useRef(0)
  const proved = useRef(false)
  const hasMachine = useRef(false)
  const calm = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // The facts whose check did not finish in the last proof, each with the time
  // of the evidence it still stands on. The state alone does not say this.
  const late = useRef<ReadonlyMap<string, string>>(new Map())
  /** Takes note of what a proof could not finish; a proof of everything starts the list again. */
  const note = (v: Verified, whole: boolean) => {
    const next = new Map(whole ? [] : late.current)
    for (const f of v.facts ?? []) {
      if (f.late) next.set(f.id, f.checked_at ?? '')
      else next.delete(f.id)
    }
    late.current = next
  }

  /** Runs one call, and keeps the "not answering" banner honest. */
  const reach = useCallback(async <T,>(work: () => Promise<T>): Promise<T> => {
    try {
      const out = await work()
      setDown(null)
      return out
    } catch (e) {
      if (!(e instanceof Refusal)) setDown(e instanceof Error ? e.message : String(e))
      throw e
    }
  }, [])

  const refresh = useCallback(async () => {
    const mine = ++asked.current
    const s = marked(await reach(() => call<State>('state')), late.current)
    if (mine < taken.current) return // an answer to a later question is already on screen
    taken.current = mine
    offset.current = ms(s.now) - Date.now()
    const ids = before.current.length ? changed(before.current, s.facts) : []
    if (ids.length) {
      setMoved(new Set(ids))
      clearTimeout(calm.current)
      calm.current = setTimeout(() => setMoved(new Set()), FLASH)
    }
    before.current = s.facts
    const now = s.facts.map((f) => f.id + f.status + (f.late ? '!' : '')).join() + '|' + (s.gone?.length ?? 0)
    if (now !== shape.current) {
      shape.current = now
      setRev((r) => r + 1)
    }
    setState(s)
    setClock(Date.now())
    if (s.demo && !hasMachine.current) {
      hasMachine.current = true
      try {
        setMachine(await call<Machine>('machine'))
      } catch {
        hasMachine.current = false
      }
    }
  }, [reach])

  /** Does one thing at a time: the regular look-in waits while an action runs. */
  const act = useCallback(async <T,>(work: () => Promise<T>): Promise<T> => {
    busy.current++
    try {
      return await work()
    } finally {
      busy.current--
    }
  }, [])

  const proveAll = useCallback(
    () =>
      act(async () => {
        setProving(true)
        try {
          note(await reach(() => call<Verified>('verify', {})), true)
          await refresh()
          proved.current = true
          setProvedAt(Date.now() + offset.current)
          setSweep((n) => n + 1)
          setRev((r) => r + 1)
        } finally {
          setProving(false)
        }
      }),
    [act, reach, refresh],
  )

  const prove = useCallback(
    (id: string) =>
      act(async () => {
        note(await reach(() => call<Verified>('verify', { ids: [id] })), false)
        await refresh()
        setRev((r) => r + 1)
      }),
    [act, reach, refresh],
  )

  const remember = useCallback(
    (d: Draft) =>
      act(async () => {
        const out = await reach(() => call<{ fact: View; already: boolean }>('remember', { ...d }))
        await refresh()
        return out
      }),
    [act, reach, refresh],
  )

  const retract = useCallback(
    (id: string, reason: string) =>
      act(async () => {
        await reach(() => call('retract', { id, reason }))
        await refresh()
      }),
    [act, reach, refresh],
  )

  const vouch = useCallback(
    (id: string) =>
      act(async () => {
        await reach(() => call('vouch', { id }))
        await refresh()
      }),
    [act, reach, refresh],
  )

  /** Changes the pretend machine, then has everything proved again. */
  const change = useCallback(
    (id: string | null) =>
      act(async () => {
        setFlipping(id ?? '*')
        try {
          setMachine(await reach(() => (id ? call<Machine>('flip', { id }) : call<Machine>('reset', {}))))
          note(await reach(() => call<Verified>('verify', {})), true)
          await refresh()
          setProvedAt(Date.now() + offset.current)
          setRev((r) => r + 1)
          setLed((l) => ({ id, n: l.n + 1 }))
        } finally {
          setFlipping(null)
        }
      }),
    [act, reach, refresh],
  )
  const flip = useCallback((id: string) => change(id), [change])
  const reset = useCallback(() => change(null), [change])

  // Open: show what is known at once, then prove it.
  useEffect(() => {
    const quiet = () => {}
    void (async () => {
      try {
        await ready()
        await refresh()
        await proveAll()
      } catch (e) {
        if (!(e instanceof Refusal)) setDown(e instanceof Error ? e.message : String(e))
      }
    })()

    // Agents change the ledger while the page is open: look in while it is on screen.
    const look = setInterval(() => {
      if (document.hidden || busy.current) return
      if (proved.current) refresh().catch(quiet)
      else proveAll().catch(quiet)
    }, POLL)
    const tick = setInterval(() => setClock(Date.now()), 1_000)

    // Back after a while away: what was proved then says little about now.
    let left = 0
    const seen = () => {
      if (document.hidden) {
        left = Date.now()
        return
      }
      if (busy.current) return
      if (left && Date.now() - left > AWAY) proveAll().catch(quiet)
      else refresh().catch(quiet)
    }
    document.addEventListener('visibilitychange', seen)
    return () => {
      clearInterval(look)
      clearInterval(tick)
      clearTimeout(calm.current)
      document.removeEventListener('visibilitychange', seen)
    }
  }, [refresh, proveAll])

  return {
    state,
    machine,
    now: clock + offset.current,
    provedAt,
    proving,
    flipping,
    led,
    moved,
    sweep,
    rev,
    down,
    refresh,
    proveAll,
    prove,
    remember,
    retract,
    vouch,
    flip,
    reset,
  }
}
