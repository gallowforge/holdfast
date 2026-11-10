// What an agent is told when it asks. In the demo the same question is also
// answered by a plain notes file, which is what most agents have to go on.
import { useEffect, useRef, useState } from 'preact/hooks'
import { PaneHead } from './bits'
import { lines, showAs, type Line } from './derive'
import { ago } from './format'
import { Glyph, Icon } from './glyphs'
import { recall } from './ledger'
import type { Machine, Recalled, Shown, State, View } from './types'

interface Props {
  state: State
  machine: Machine | null
  /** The switch flipped last, in the demo. */
  led: { id: string | null; n: number }
  rev: number
  now: number
  all: ReadonlyMap<string, View>
  onOpen(id: string): void
}

/** What the notes file's line is worth, given what the fact behind it is now. */
const VERDICT: Record<Shown, string> = {
  holds: 'Still true today. The file has no way to know.',
  broken: 'No longer true. Nothing in the file says so.',
  late: 'Unknown just now. The file states it as fact all the same.',
  blocked: 'Unknown. The file states it as fact all the same.',
  stale: 'Never proved. The file states it as fact all the same.',
  unproven: 'Never proved. The file states it as fact all the same.',
}

export function RecallPane({ state, machine, led, rev, now, all, onOpen }: Props) {
  const notes = state.demo ? (machine?.notes ?? []) : []
  // The question about the thing that was just changed, when there is one.
  const about = machine?.switches.find((s) => s.id === led.id)?.facts ?? []
  const follows = notes.findIndex((n) => about.includes(n.fact))
  const [pick, setPick] = useState(() => Math.max(0, follows))
  const [typed, setTyped] = useState('')
  // The query typed by hand, once entered; null while a prepared question is asked.
  const [own, setOwn] = useState<string | null>(null)
  // Change the machine and the pane asks about what changed: that answer is the point.
  const seen = useRef(led.n)
  useEffect(() => {
    if (seen.current === led.n) return
    seen.current = led.n
    if (follows < 0) return
    setPick(follows)
    setOwn(null)
    setTyped('')
  }, [led.n, follows])

  const note = own === null ? notes[pick] : undefined
  const query = own ?? note?.query ?? ''
  // A prepared question is answered with the one fact that matches best, to
  // set beside the one line of the notes file.
  const limit = note ? 1 : 0
  const waiting = state.demo && !machine

  const [answer, setAnswer] = useState<{ query: string; told: Recalled } | null>(null)
  const [failed, setFailed] = useState<string | null>(null)
  const turn = useRef(0)
  useEffect(() => {
    if (waiting) return
    const mine = ++turn.current
    recall(query, limit).then(
      (told) => {
        if (mine !== turn.current) return
        setAnswer({ query, told })
        setFailed(null)
      },
      (err: unknown) => {
        if (mine !== turn.current) return
        setFailed(err instanceof Error ? err.message : String(err))
      },
    )
  }, [query, limit, rev, waiting])

  const enter = (e: Event) => {
    e.preventDefault()
    const q = typed.trim()
    if (q === '' && notes.length > 0) setOwn(null)
    else setOwn(q)
  }
  const fact = note ? all.get(note.fact) : undefined
  const old = answer !== null && answer.query !== query

  return (
    <>
      <PaneHead title="What an agent is told">
        <p>{state.demo ? 'One question, answered by a notes file and by Holdfast.' : 'Every fact in the answer is proved again first.'}</p>
      </PaneHead>
      <div class="pane-body recall">
        {notes.length > 0 && (
          <ul class="asks" aria-label="Questions an agent asks">
            {notes.map((n, i) => {
              const about = all.get(n.fact)
              const status = about && showAs(about)
              return (
                <li key={n.query}>
                  <button
                    type="button"
                    class="ask"
                    aria-pressed={own === null && i === pick}
                    onClick={() => {
                      setPick(i)
                      setOwn(null)
                      setTyped('')
                    }}
                  >
                    <span>{n.question}</span>
                    {status && <Glyph status={status} size={12} />}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
        <form class="askbox" onSubmit={enter} role="search">
          <Icon name="search" size={14} />
          <input
            type="text"
            value={typed}
            maxLength={300}
            enterkeyhint="search"
            autocomplete="off"
            spellcheck={false}
            placeholder={notes.length > 0 ? 'Or ask in your own words, then press Enter' : 'Ask as an agent would, then press Enter'}
            aria-label="Ask as an agent would"
            onInput={(e) => setTyped(e.currentTarget.value)}
          />
        </form>

        {note && machine && (
          <section class="said said-file">
            <header>
              <h3>A notes file</h3>
              <span>written {ago(now, machine.written)}</span>
            </header>
            <p class="file">- {note.line}</p>
            {fact && (
              <p class={`verdict t-${showAs(fact)}`}>
                <Glyph status={showAs(fact)} size={14} />
                <span>{VERDICT[showAs(fact)]}</span>
              </p>
            )}
          </section>
        )}

        <section class={`said said-holdfast${old ? ' is-old' : ''}`} aria-live="polite">
          <header>
            <h3>Holdfast</h3>
            <code>{query === '' ? 'recall' : `recall "${query}"`}</code>
          </header>
          {failed ? (
            <p class="refusal">{failed}</p>
          ) : answer ? (
            <Told text={answer.told.text} onOpen={onOpen} />
          ) : (
            <p class="quiet">Proving the answer…</p>
          )}
        </section>
      </div>
    </>
  )
}

const ADVICE = /^(BROKEN|TIMED OUT|BLOCKED|UNPROVEN and STALE): (.*)$/
const ADVICE_TONE: Record<string, Shown> = { BROKEN: 'broken', 'TIMED OUT': 'late', BLOCKED: 'blocked', 'UNPROVEN and STALE': 'unproven' }
const HEAD = /^(f-[a-z0-9]+)\s+(.*)$/

/** The text an agent reads, word for word, with each status in its colour. */
function Told({ text, onOpen }: { text: string; onOpen(id: string): void }) {
  return (
    <div class="told">
      {lines(text).map((l, i) => (
        <ToldLine key={i} line={l} onOpen={onOpen} />
      ))}
    </div>
  )
}

function ToldLine({ line, onOpen }: { line: Line; onOpen(id: string): void }) {
  if (line.label) {
    const m = HEAD.exec(line.text)
    return (
      <div class="ln ln-head">
        <b class={`lab t-${line.tone}`}>{line.label.trim()}</b>
        {m ? (
          <span>
            <button type="button" class="idlink" onClick={() => onOpen(m[1]!)} title="Open this fact">
              {m[1]}
            </button>{' '}
            {m[2]}
          </span>
        ) : (
          <span>{line.text}</span>
        )}
      </div>
    )
  }
  if (line.text.trim() === '') return <div class="ln ln-gap" />
  if (line.text.startsWith(' ')) return <div class="ln ln-more">{line.text.trim()}</div>
  const a = ADVICE.exec(line.text)
  if (a) {
    return (
      <div class="ln ln-advice">
        <b class={`t-${ADVICE_TONE[a[1]!]}`}>{a[1]}:</b> {a[2]}
      </div>
    )
  }
  return <div class="ln">{line.text}</div>
}
