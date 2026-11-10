// The pane beside the ledger. It shows one thing at a time: what an agent is
// told, one fact in full, the form for a new fact, or the policy.
import type { ComponentChildren } from 'preact'
import { useMemo } from 'preact/hooks'
import { PaneHead } from './bits'
import type { Ledger } from './ledger'
import { FactPane } from './pane-fact'
import { NewPane } from './pane-new'
import { PolicyPane } from './pane-policy'
import { RecallPane } from './pane-recall'
import type { State, View } from './types'

export type PaneMode =
  | { kind: 'recall' }
  | { kind: 'fact'; id: string }
  | { kind: 'new'; replaces?: string }
  | { kind: 'policy' }

interface Props {
  ledger: Ledger
  state: State
  mode: PaneMode
  onMode(m: PaneMode): void
}

export function Pane({ ledger, state, mode, onMode }: Props) {
  const all = useMemo(() => {
    const m = new Map<string, View>()
    for (const v of state.gone ?? []) m.set(v.id, v)
    for (const v of state.facts) m.set(v.id, v)
    return m
  }, [state])
  const home = () => onMode({ kind: 'recall' })
  const open = (id: string) => onMode({ kind: 'fact', id })

  let body: ComponentChildren
  switch (mode.kind) {
    case 'fact': {
      const fact = all.get(mode.id)
      body = fact ? (
        <FactPane key={fact.id} fact={fact} all={all} ledger={ledger} onClose={home} onOpen={open} onMode={onMode} />
      ) : (
        <Gone onClose={home} />
      )
      break
    }
    case 'new':
      body = (
        <NewPane
          key="new"
          replaces={mode.replaces ? all.get(mode.replaces) : undefined}
          ledger={ledger}
          onClose={mode.replaces ? () => open(mode.replaces!) : home}
          onKeepAlone={() => onMode({ kind: 'new' })}
          onDone={open}
        />
      )
      break
    case 'policy':
      body = <PolicyPane state={state} onClose={home} />
      break
    default:
      body = <RecallPane state={state} machine={ledger.machine} led={ledger.led} rev={ledger.rev} now={ledger.now} all={all} onOpen={open} />
  }
  return (
    <aside class="pane" data-mode={mode.kind} aria-label="Details">
      {body}
    </aside>
  )
}

function Gone({ onClose }: { onClose(): void }) {
  return (
    <>
      <PaneHead title="That fact is not here" onClose={onClose} />
      <div class="pane-body">
        <p class="quiet">Nothing in this ledger has that id.</p>
      </div>
    </>
  )
}
