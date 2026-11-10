// The ledger: every fact, what its status rests on, and how long ago that was
// seen. What needs a person comes first.
import type { RefObject } from 'preact'
import { useRef } from 'preact/hooks'
import { Command } from './bits'
import { aside, basis, headline, ink, lifeSpent, ORDER, showAs, tally, type Group } from './derive'
import { useGlide } from './flip'
import { ago } from './format'
import { Glyph, Icon } from './glyphs'
import type { Counts, Shown, State, View } from './types'

const COUNTED: Record<Shown, [one: string, many: string]> = {
  broken: ['broken', 'broken'],
  late: ['timed out', 'timed out'],
  blocked: ['blocked', 'blocked'],
  stale: ['stale', 'stale'],
  unproven: ['unproven', 'unproven'],
  holds: ['holds', 'hold'],
}

interface Props {
  state: State
  now: number
  groups: Group[]
  gone: View[]
  selected: string | null
  moved: ReadonlySet<string>
  sweep: number
  proving: boolean
  query: string
  only: Shown | null
  filter: RefObject<HTMLInputElement | null>
  onQuery(q: string): void
  onOnly(s: Shown | null): void
  onSelect(id: string): void
  onAdd(): void
  onPolicy(): void
}

export function Ledger(p: Props) {
  const { state, now, groups } = p
  const c = state.counts
  const head = headline(c)
  const root = useRef<HTMLElement>(null)
  const order = groups.flatMap((g) => g.facts.map((f) => f.id)).join()
  useGlide(root, order)

  const filtered = p.query.trim() !== '' || p.only !== null
  let index = 0
  return (
    <section class={`ledger${p.proving ? ' is-proving' : ''}`} ref={root} aria-label="The ledger">
      <header class="summary">
        <h1 class={`headline t-${head.tone}`}>{head.text}</h1>
        {c.total > 0 && (
          <>
            <StateBar counts={c} />
            <div class="tools">
              <div class="legend" role="group" aria-label="Show only">
                <button type="button" class="pick" aria-pressed={p.only === null} onClick={() => p.onOnly(null)}>
                  <b>{c.total}</b> {c.total === 1 ? 'fact' : 'facts'}
                </button>
                {ORDER.filter((s) => tally(c, s) > 0 || s === 'broken').map((s) => {
                  const n = tally(c, s)
                  return (
                    <button
                      type="button"
                      key={s}
                      class={`pick t-${s}${n === 0 ? ' is-zero' : ''}`}
                      aria-pressed={p.only === s}
                      disabled={n === 0 && p.only !== s}
                      onClick={() => p.onOnly(p.only === s ? null : s)}
                    >
                      <Glyph status={s} size={14} />
                      <b>{n}</b> {COUNTED[s][n === 1 ? 0 : 1]}
                    </button>
                  )
                })}
              </div>
              <label class="filter">
                <Icon name="search" size={14} />
                <input
                  ref={p.filter}
                  type="search"
                  value={p.query}
                  onInput={(e) => p.onQuery(e.currentTarget.value)}
                  placeholder="Filter facts"
                  aria-label="Filter facts"
                  autocomplete="off"
                  spellcheck={false}
                />
                <kbd aria-hidden="true">/</kbd>
              </label>
            </div>
          </>
        )}
      </header>

      {c.total === 0 ? (
        <Start onAdd={p.onAdd} />
      ) : groups.length === 0 ? (
        <p class="nothing">
          No fact matches that.{' '}
          <button
            type="button"
            class="link"
            onClick={() => {
              p.onQuery('')
              p.onOnly(null)
            }}
          >
            Show them all
          </button>
        </p>
      ) : (
        groups.map((g) => (
          <section class={`group g-${g.key}`} key={g.key}>
            <h2>
              {g.title} <span class="n">{g.facts.length}</span>
              <span class="hint">{g.hint}</span>
            </h2>
            <ol>
              {g.facts.map((v) => (
                <Row key={v.id} v={v} now={now} index={index++} selected={p.selected === v.id} moved={p.moved.has(v.id)} sweep={p.sweep} onSelect={p.onSelect} />
              ))}
            </ol>
          </section>
        ))
      )}

      {p.gone.length > 0 && !filtered && (
        <details class="group g-gone">
          <summary>
            Withdrawn <span class="n">{p.gone.length}</span>
            <span class="hint">retracted or replaced, kept for the record</span>
          </summary>
          <ol>
            {p.gone.map((v) => (
              <Row key={v.id} v={v} now={now} index={0} selected={p.selected === v.id} moved={false} sweep={0} onSelect={p.onSelect} />
            ))}
          </ol>
        </details>
      )}

      <footer class="foot">
        <button type="button" class="link" onClick={p.onPolicy}>
          Policy
        </button>
        <span>{state.approved ? 'in force on this machine' : 'not in force on this machine'}</span>
        <span class="ver">Holdfast {state.version}</span>
      </footer>
    </section>
  )
}

/** The whole store as one thin line, so its state reads before any word does. */
function StateBar({ counts }: { counts: Counts }) {
  return (
    <div class="statebar" aria-hidden="true">
      {ORDER.map((s) => (
        <i key={s} class={`t-${s}`} style={`flex-grow:${tally(counts, s)}`} />
      ))}
    </div>
  )
}

interface RowProps {
  v: View
  now: number
  index: number
  selected: boolean
  moved: boolean
  sweep: number
  onSelect(id: string): void
}

function Row({ v, now, index, selected, moved, sweep, onSelect }: RowProps) {
  const gone = v.retracted !== undefined || !!v.replaced_by
  const tone = gone ? 'gone' : showAs(v)
  const note = !gone && !v.check
  let second = basis(v, now)
  let side = aside(v, now)
  if (v.retracted) {
    second = `retracted ${ago(now, v.retracted.at)}${v.retracted.by ? ` by ${v.retracted.by}` : ''}: ${v.retracted.reason ?? ''}`
    side = ''
  } else if (v.replaced_by) {
    second = `replaced by ${v.replaced_by}`
    side = ''
  }
  const cls = ['row', `t-${tone}`]
  if (selected) cls.push('is-selected')
  if (moved) cls.push('is-moved')
  if (sweep > 0 && v.check && !gone) cls.push(`sweep-${sweep % 2}`)
  return (
    <li class={cls.join(' ')} data-flip={v.id} style={`--i:${index};--ink:${ink(v, now).toFixed(3)}`}>
      <button type="button" class="row-main" onClick={() => onSelect(v.id)} aria-current={selected ? 'true' : undefined}>
        <Glyph status={tone} title={gone ? 'Withdrawn' : tone === 'late' ? 'timed out' : v.status} />
        <span class="row-text">
          <span class="claim">
            <span class="strike">{v.claim}</span>
          </span>
          <span class="basis">{second}</span>
          {note && (
            <span class="life" aria-hidden="true">
              <i style={`width:${Math.min(100, Math.round(lifeSpent(v, now) * 100))}%`} />
            </span>
          )}
        </span>
        <span class="row-meta">
          {v.by && <span class="by">{v.by}</span>}
          {side && <span class="aside">{side}</span>}
        </span>
      </button>
    </li>
  )
}

/** An empty ledger: the three ways a first fact gets in. */
function Start({ onAdd }: { onAdd(): void }) {
  return (
    <div class="start">
      <p class="lead">A fact is a claim in plain words, and a check that proves it. Three ways to store the first one:</p>
      <ol>
        <li>
          <span class="how">From a terminal</span>
          <Command text={'holdfast add "The API listens on port 8080" --check "port localhost:8080"'} />
        </li>
        <li>
          <span class="how">From an agent, once it is connected</span>
          <Command text="claude mcp add holdfast -- holdfast mcp" />
        </li>
        <li>
          <span class="how">From here</span>
          <button type="button" class="btn" onClick={onAdd}>
            <Icon name="plus" size={14} /> Add a fact
          </button>
        </li>
      </ol>
    </div>
  )
}
