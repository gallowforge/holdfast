// The page: a bar, the ledger, and a pane beside it. In the demo a rail on
// the left changes the pretend machine the facts are about.
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { groups as grouped, passes } from './derive'
import { ago } from './format'
import { Icon, Mark } from './glyphs'
import { useLedger, type Ledger } from './ledger'
import { Ledger as LedgerColumn } from './list'
import { MachineRail } from './machine'
import { Pane, type PaneMode } from './pane'
import { useTheme, type Mode } from './theme'
import type { Shown, State } from './types'

const SOURCE = 'https://github.com/gallowforge/holdfast'

/** Which column a phone shows; wider screens show them all. */
type Tab = 'ledger' | 'machine' | 'recall'

const THEME_ICON = { system: 'system', light: 'sun', dark: 'moon' } as const
const THEME_SAYS: Record<Mode, string> = {
  system: 'Theme: follows the system',
  light: 'Theme: light',
  dark: 'Theme: dark',
}

export function App() {
  const ledger = useLedger()
  const { state } = ledger
  const [theme, cycle] = useTheme()

  const [mode, setMode] = useState<PaneMode>({ kind: 'recall' })
  const [tab, setTab] = useState<Tab>('ledger')
  const [query, setQuery] = useState('')
  const [only, setOnly] = useState<Shown | null>(null)
  const filter = useRef<HTMLInputElement>(null)
  const byKey = useRef(false)

  const facts = state?.facts
  const shown = useMemo(() => grouped((facts ?? []).filter((v) => passes(v, query, only))), [facts, query, only])
  const ids = useMemo(() => shown.flatMap((g) => g.facts.map((v) => v.id)), [shown])
  const byId = useMemo(() => new Map((facts ?? []).map((v) => [v.id, v] as const)), [facts])
  const gone = useMemo(
    () => (only ? [] : (state?.gone ?? []).filter((v) => passes(v, query, null))),
    [state?.gone, query, only],
  )
  const selected = mode.kind === 'fact' ? mode.id : null

  // The keyboard: arrows or j and k walk the ledger, / finds, Esc steps back.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const el = e.target instanceof HTMLElement ? e.target : null
      const typing = el !== null && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)
      if (e.key === 'Escape') {
        if (typing) {
          if (el === filter.current) setQuery('')
          el.blur()
        } else if (mode.kind !== 'recall') {
          setMode({ kind: 'recall' })
        }
        return
      }
      if (typing) return
      if (e.key === '/') {
        e.preventDefault()
        setTab('ledger')
        filter.current?.focus()
        return
      }
      const step = e.key === 'ArrowDown' || e.key === 'j' ? 1 : e.key === 'ArrowUp' || e.key === 'k' ? -1 : 0
      if (step === 0 || ids.length === 0) return
      e.preventDefault()
      const at = selected === null ? -1 : ids.indexOf(selected)
      const next = at < 0 ? (step > 0 ? 0 : ids.length - 1) : Math.min(ids.length - 1, Math.max(0, at + step))
      byKey.current = true
      setMode({ kind: 'fact', id: ids[next]! })
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [ids, selected, mode.kind])

  useEffect(() => {
    if (!byKey.current) return
    byKey.current = false
    document.querySelector('.row.is-selected')?.scrollIntoView({ block: 'nearest' })
  }, [selected])

  const broken = state?.counts.broken ?? 0
  const project = state?.project ?? ''
  useEffect(() => {
    if (project === '') return
    document.title = `${broken > 0 ? `(${broken} broken) ` : ''}${project} · Holdfast`
  }, [broken, project])

  if (!state) {
    return (
      <div class="app is-boot">
        <Bar ledger={ledger} state={null} theme={theme} onTheme={cycle} onAdd={() => {}} />
        <main class="boot">
          {ledger.down ? (
            <>
              <p class="boot-line">Holdfast is not answering.</p>
              <p class="quiet">{ledger.down}</p>
              <button type="button" class="btn" onClick={() => void ledger.refresh()}>
                Try again
              </button>
            </>
          ) : (
            <p class="boot-line is-waiting">Opening the ledger</p>
          )}
        </main>
      </div>
    )
  }

  const open = (id: string) => setMode({ kind: 'fact', id })
  const toggle = (id: string) => setMode(selected === id ? { kind: 'recall' } : { kind: 'fact', id })
  const add = () => setMode({ kind: 'new' })

  return (
    <div class="app" data-demo={state.demo ? '' : undefined} data-view={tab} data-pane={mode.kind}>
      <Bar ledger={ledger} state={state} theme={theme} onTheme={cycle} onAdd={add} />

      {state.note && (
        <p class="banner" role="status">
          {state.note}
        </p>
      )}
      {ledger.down && (
        <p class="banner banner-down" role="alert">
          <span>The engine is not answering, so this may be out of date. {ledger.down}</span>
          <button type="button" class="link" onClick={() => void ledger.refresh()}>
            Try again
          </button>
        </p>
      )}

      <nav class="tabs" aria-label="Views">
        <button type="button" aria-pressed={tab === 'ledger'} onClick={() => setTab('ledger')}>
          Ledger
        </button>
        {state.demo && (
          <button type="button" aria-pressed={tab === 'machine'} onClick={() => setTab('machine')}>
            Machine
          </button>
        )}
        <button type="button" aria-pressed={tab === 'recall'} onClick={() => setTab('recall')}>
          Recall
        </button>
      </nav>

      <main class="cols">
        {state.demo &&
          (ledger.machine ? (
            <MachineRail
              machine={ledger.machine}
              facts={byId}
              flipping={ledger.flipping}
              onFlip={(id) => void ledger.flip(id)}
              onReset={() => void ledger.reset()}
              onSelect={open}
            />
          ) : (
            <aside class="rail" />
          ))}
        <LedgerColumn
          state={state}
          now={ledger.now}
          groups={shown}
          gone={gone}
          selected={selected}
          moved={ledger.moved}
          sweep={ledger.sweep}
          proving={ledger.proving}
          query={query}
          only={only}
          filter={filter}
          onQuery={setQuery}
          onOnly={setOnly}
          onSelect={toggle}
          onAdd={add}
          onPolicy={() => setMode({ kind: 'policy' })}
        />
        <Pane ledger={ledger} state={state} mode={mode} onMode={setMode} />
      </main>
    </div>
  )
}

interface BarProps {
  ledger: Ledger
  state: State | null
  theme: Mode
  onTheme(): void
  onAdd(): void
}

function Bar({ ledger, state, theme, onTheme, onAdd }: BarProps) {
  // With nothing stored there is nothing to prove, and no time it was proved at.
  const empty = !state || state.counts.total === 0
  // "Proved" is said only when every check finished; otherwise the bar says what was run, not what was shown.
  const unfinished = (state?.counts.late ?? 0) > 0
  const proved = empty
    ? ''
    : ledger.proving
      ? 'Proving'
      : ledger.provedAt !== null
        ? `${unfinished ? 'Not all proved' : 'Proved'} ${ago(ledger.now, ledger.provedAt)}`
        : ''
  return (
    <header class="bar">
      <div class="brand">
        <Mark size={20} />
        <strong>Holdfast</strong>
        {state && (
          <>
            <span class="slash" aria-hidden="true">
              /
            </span>
            <span class="project">{state.project}</span>
            {state.demo && <span class="demo-tag">live demo</span>}
          </>
        )}
      </div>
      <div class="bar-actions">
        {state && (
          <span class={`proved${ledger.proving ? ' is-waiting' : ''}`} role="status">
            {proved}
          </span>
        )}
        <button
          type="button"
          class="btn btn-primary"
          disabled={empty || ledger.proving}
          onClick={() => void ledger.proveAll()}
          title="Run every check again"
        >
          <Icon name="again" size={14} />
          <span>Prove all</span>
        </button>
        <button type="button" class="btn bar-add" disabled={!state} onClick={onAdd} aria-label="Add a fact" title="Add a fact">
          <Icon name="plus" size={14} />
          <span>Add a fact</span>
        </button>
        <button type="button" class="icon-btn" onClick={onTheme} aria-label={`${THEME_SAYS[theme]}. Change it.`} title={THEME_SAYS[theme]}>
          <Icon name={THEME_ICON[theme]} />
        </button>
        {state?.demo && (
          <a class="btn btn-quiet bar-source" href={SOURCE} target="_blank" rel="noopener">
            <span>Source</span>
            <Icon name="out" size={12} />
          </a>
        )}
      </div>
    </header>
  )
}
