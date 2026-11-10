// The demo's pretend machine: the things a real one does behind an agent's
// back. Each change is made in the engine's own simulated world, and every
// fact is then proved again against it.
import { Glyph, Icon } from './glyphs'
import type { Machine, Switch, View } from './types'

interface Props {
  machine: Machine
  facts: ReadonlyMap<string, View>
  flipping: string | null
  onFlip(id: string): void
  onReset(): void
  onSelect(id: string): void
}

const PARTS: readonly [group: Switch['group'], title: string][] = [
  ['machine', 'On the machine'],
  ['policy', 'In the policy'],
]

export function MachineRail({ machine, facts, flipping, onFlip, onReset, onSelect }: Props) {
  const any = machine.switches.some((s) => s.on)
  return (
    <aside class="rail" aria-label="The machine">
      <header class="rail-head">
        <h2>Change the machine</h2>
        <p>
          Holdfast is a memory for coding agents in which every fact carries a check that proves it. This machine is a pretend one, inside your browser. Change
          something an agent would never hear about, and see which facts stop being true.
        </p>
      </header>
      {PARTS.map(([group, title]) => {
        const list = machine.switches.filter((s) => s.group === group)
        if (list.length === 0) return null
        return (
          <section class="switches" key={group}>
            <h3>{title}</h3>
            <ul>
              {list.map((s) => (
                <li key={s.id} class={`switch${s.on ? ' is-on' : ''}${flipping === s.id ? ' is-busy' : ''}`}>
                  <button type="button" class="switch-main" onClick={() => onFlip(s.id)} disabled={flipping !== null} aria-pressed={s.on}>
                    <span class="switch-label">
                      {s.on && <Icon name="undo" size={14} />}
                      {s.on ? s.undo : s.do}
                    </span>
                    {!s.on && (
                      <span class="switch-facts" aria-hidden="true">
                        {s.facts.map((id) => {
                          const v = facts.get(id)
                          return v ? <Glyph key={id} status={v.status} size={12} /> : null
                        })}
                      </span>
                    )}
                  </button>
                  {s.on && (
                    <div class="switch-after">
                      <p class="what">{s.what}</p>
                      <ul>
                        {s.facts.map((id) => {
                          const v = facts.get(id)
                          if (!v) return null
                          return (
                            <li key={id}>
                              <button type="button" class={`after t-${v.status}`} onClick={() => onSelect(id)}>
                                <Glyph status={v.status} size={12} title={v.status} />
                                <span>{v.claim}</span>
                              </button>
                            </li>
                          )
                        })}
                      </ul>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )
      })}
      {any && (
        <button type="button" class="btn rail-reset" onClick={onReset} disabled={flipping !== null}>
          <Icon name="undo" size={14} /> Put everything back
        </button>
      )}
    </aside>
  )
}
