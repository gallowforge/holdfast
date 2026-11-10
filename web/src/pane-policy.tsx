// The policy: what a check is allowed to touch on this machine. It is shown,
// never edited, because only a person at a terminal can widen it.
import { Command, PaneHead } from './bits'
import type { State } from './types'

const KINDS: readonly (readonly [word: string, means: string, none: string])[] = [
  ['files', 'the folders a check may read', 'no folders'],
  ['net', 'the hosts a check may connect to', 'no hosts'],
  ['cmd', 'the commands a check may run, argument by argument', 'no commands'],
]

/** The rule lines of a policy file, by their first word; comments are left out. */
function rules(text: string): Map<string, string[]> {
  const by = new Map<string, string[]>()
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '' || line.startsWith('#')) continue
    const cut = line.search(/\s/)
    const word = cut < 0 ? line : line.slice(0, cut)
    const rest = cut < 0 ? '' : line.slice(cut).trim()
    by.set(word, [...(by.get(word) ?? []), rest])
  }
  return by
}

export function PolicyPane({ state, onClose }: { state: State; onClose(): void }) {
  const by = rules(state.policy)
  const other = [...by.keys()].filter((word) => !KINDS.some(([known]) => known === word))
  return (
    <>
      <PaneHead title="Policy" onClose={onClose}>
        <p>What a check may touch on this machine.</p>
      </PaneHead>
      <div class="pane-body policy">
        {by.size === 0 ? (
          <p class="quiet">
            There is no policy file here, so the defaults apply. <code>holdfast policy</code> prints them.
          </p>
        ) : (
          <dl class="rules">
            {KINDS.map(([word, means, none]) => (
              <div class="rule" key={word}>
                <dt>
                  <code>{word}</code>
                </dt>
                <dd>
                  {by.get(word)?.map((rest, i) => <code key={i}>{rest}</code>) ?? <span class="none">{none}</span>}
                  <small>{means}</small>
                </dd>
              </div>
            ))}
            {other.map((word) => (
              <div class="rule" key={word}>
                <dt>
                  <code>{word}</code>
                </dt>
                <dd>
                  {by.get(word)?.map((rest, i) => <code key={i}>{rest}</code>)}
                  <small>not a rule Holdfast knows, so it changes nothing</small>
                </dd>
              </div>
            ))}
          </dl>
        )}

        {state.approved ? (
          <p class="standing good">In force. A person approved this policy on this machine.</p>
        ) : (
          <div class="standing bad">
            <p>Not in force. Nobody has approved this policy on this machine, so the defaults apply.</p>
            <Command text="holdfast trust" />
          </div>
        )}

        <p class="quiet">
          A check that reaches outside the policy is not run. Its fact is shown as blocked, with the line that would allow it. An agent cannot
          widen the policy through Holdfast; a person does that, in a terminal.
        </p>
        {state.demo && <p class="quiet">In this demo the switch “Allow billing.example.net” is that person.</p>}
      </div>
    </>
  )
}
