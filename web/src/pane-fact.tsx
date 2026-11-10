// One fact in full: what its status rests on, the check that proves it, and
// everything that has happened to it.
import { useEffect, useRef, useState } from 'preact/hooks'
import { Command, Copy } from './bits'
import { lifeSpent, MEANS, noted, pasteable, showAs, splitFix, WORD } from './derive'
import { age, ago, ms, stamp, took } from './format'
import { Glyph, Icon, type Tone } from './glyphs'
import type { Ledger } from './ledger'
import type { PaneMode } from './pane'
import type { View } from './types'

interface Props {
  fact: View
  all: ReadonlyMap<string, View>
  ledger: Ledger
  onClose(): void
  onOpen(id: string): void
  onMode(m: PaneMode): void
}

const PROOF_TITLE = { holds: 'Proof', broken: 'Proof failed', late: 'Last proof', blocked: 'Not checked', stale: 'Proof', unproven: 'Proof' } as const
const FOR = { holds: 'holding for', broken: 'broken for', blocked: 'blocked for', stale: 'stale for', unproven: 'unproven for' } as const

export function FactPane({ fact, all, ledger, onClose, onOpen, onMode }: Props) {
  const now = ledger.now
  const gone = fact.retracted !== undefined || Boolean(fact.replaced_by)
  const as = showAs(fact)
  const tone: Tone = gone ? 'gone' : as
  const word = fact.retracted ? 'Retracted' : fact.replaced_by ? 'Replaced' : WORD[as]
  const means = fact.retracted
    ? 'Withdrawn by a person or an agent. It is kept, with the reason.'
    : fact.replaced_by
      ? 'A newer fact took its place.'
      : MEANS[as]

  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const run = async (name: string, job: () => Promise<unknown>) => {
    setBusy(name)
    setError(null)
    try {
      await job()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  const { why, fix } = splitFix(fact.detail, fact.fix)
  const meta: string[] = []
  if (fact.check && !gone) {
    if (fact.late) meta.push('the check did not finish in time just now')
    if (fact.checked_at) meta.push(`${fact.late ? 'last checked' : 'checked'} ${ago(now, fact.checked_at)}`)
    if (fact.status !== 'blocked' && fact.ms !== undefined) meta.push(`took ${took(fact.ms)}`)
    if (fact.since && as !== 'late') meta.push(`${FOR[fact.status]} ${age(now - ms(fact.since))}`)
    if (fact.status === 'broken') meta.push(fact.held ? `last held ${ago(now, fact.held)}` : 'never held')
  }
  const history = [...(fact.history ?? [])].reverse()
  const older = fact.replaces ? all.get(fact.replaces) : undefined

  return (
    <>
      <header class="pane-head">
        <div class={`fact-status t-${tone}`}>
          <Glyph status={tone} size={18} />
          <h2>{word}</h2>
          <p>{means}</p>
        </div>
        <button type="button" class="icon-btn pane-close" onClick={onClose} aria-label="Close" title="Close (Esc)">
          <Icon name="close" />
        </button>
      </header>
      <div class="pane-body fact">
        <p class="fact-claim">{fact.claim}</p>
        <p class="fact-id">
          <code>{fact.id}</code>
          <Copy text={fact.id} label="Copy the id" />
          {(fact.tags ?? []).map((t) => (
            <span class="tag" key={t}>
              #{t}
            </span>
          ))}
        </p>

        {fact.retracted && (
          <section class="part">
            <h3>Retracted</h3>
            <p class="evidence">{fact.retracted.reason || 'No reason was given.'}</p>
            <p class="meta">
              {ago(now, fact.retracted.at)}
              {fact.retracted.by ? ` · by ${fact.retracted.by}` : ''}
            </p>
          </section>
        )}
        {fact.replaced_by && (
          <section class="part">
            <h3>Replaced by</h3>
            <IdLink id={fact.replaced_by} all={all} onOpen={onOpen} />
          </section>
        )}

        {!gone && fact.check && (
          <section class={`part proof t-${as}`}>
            <h3>{PROOF_TITLE[as]}</h3>
            <p class="evidence">{why}</p>
            {meta.length > 0 && <p class="meta">{meta.join(' · ')}</p>}
            {fact.status === 'blocked' && (
              <div class="next">
                <p>Only a person can allow the check{pasteable(fix) ? ', by running:' : fix ? `, ${fix}.` : '.'}</p>
                {pasteable(fix) && <Command text={fix} />}
                <button type="button" class="link" onClick={() => onMode({ kind: 'policy' })}>
                  See the policy
                </button>
              </div>
            )}
            {fact.status === 'broken' && (
              <div class="next">
                <p>If you know what is true now, put it in its place. This one is kept as history.</p>
                <button type="button" class="btn" onClick={() => onMode({ kind: 'new', replaces: fact.id })}>
                  Replace it…
                </button>
              </div>
            )}
          </section>
        )}

        {fact.check && (
          <section class="part">
            <h3>Check</h3>
            <div class="checkline">
              <code>{fact.check}</code>
              {!gone && (
                <button type="button" class="btn" disabled={busy !== null} onClick={() => run('prove', () => ledger.prove(fact.id))}>
                  <Icon name="again" size={14} />
                  {busy === 'prove' ? 'Proving…' : 'Prove again'}
                </button>
              )}
            </div>
          </section>
        )}

        {!gone && !fact.check && <NotePart fact={fact} now={now} busy={busy} run={run} ledger={ledger} onMode={onMode} />}

        {error && <p class="refusal">{error}</p>}

        {history.length > 0 && (
          <section class="part">
            <h3>History</h3>
            <ol class="history">
              {history.map((h) => (
                <li key={h.at + h.status} class={`t-${h.status}`}>
                  <Glyph status={h.status} size={12} />
                  <div>
                    <p>
                      <b>{WORD[h.status]}</b>
                      <time dateTime={h.at} title={ago(now, h.at)}>
                        {stamp(h.at, now)}
                      </time>
                    </p>
                    <p class="h-detail">{splitFix(h.detail, h.fix).why}</p>
                  </div>
                </li>
              ))}
            </ol>
          </section>
        )}

        <section class="part">
          <h3>Record</h3>
          <dl class="record">
            <dt>Remembered</dt>
            <dd>
              {stamp(fact.created, now)}
              {fact.by ? ` by ${fact.by}` : ''}
            </dd>
            {fact.vouched_by && fact.noted_at && (
              <>
                <dt>Confirmed</dt>
                <dd>
                  {stamp(fact.noted_at, now)} by {fact.vouched_by}
                </dd>
              </>
            )}
            {fact.replaces && (
              <>
                <dt>Replaces</dt>
                <dd>
                  <IdLink id={fact.replaces} all={all} onOpen={onOpen} />
                </dd>
              </>
            )}
            {older?.retracted && (
              <>
                <dt>Note</dt>
                <dd>the fact it replaces was retracted</dd>
              </>
            )}
          </dl>
        </section>

        {!gone && <Retract id={fact.id} ledger={ledger} />}
      </div>
    </>
  )
}

function IdLink({ id, all, onOpen }: { id: string; all: ReadonlyMap<string, View>; onOpen(id: string): void }) {
  const other = all.get(id)
  return (
    <button type="button" class="idlink" onClick={() => onOpen(id)}>
      <code>{id}</code>
      {other ? ` ${other.claim}` : ''}
    </button>
  )
}

interface NoteProps {
  fact: View
  now: number
  busy: string | null
  run(name: string, job: () => Promise<unknown>): Promise<void>
  ledger: Ledger
  onMode(m: PaneMode): void
}

/** A note has no proof to show, only how long it has gone without one. */
function NotePart({ fact, now, busy, run, ledger, onMode }: NoteProps) {
  const spent = lifeSpent(fact, now)
  const until = ms(fact.stale_at)
  const left = Number.isNaN(until)
    ? ''
    : until > now
      ? `goes stale in ${age(until - now)}`
      : `went stale ${ago(now, fact.stale_at)}`
  return (
    <section class={`part proof t-${fact.status}`}>
      <h3>No proof</h3>
      <p class="evidence">
        {fact.status === 'stale'
          ? 'A note that has outlived its time. Treat it as a guess until someone confirms it.'
          : 'A note with no check. It is only as good as whoever wrote it.'}
      </p>
      <div class="lifebar" role="img" aria-label={`${Math.round(Math.min(1, spent) * 100)}% of its life spent`}>
        <i style={{ width: `${Math.min(100, spent * 100)}%` }} />
      </div>
      <p class="meta">
        {`${age(now - noted(fact))} old`}
        {left ? ` · ${left}` : ''}
      </p>
      <div class="next actions">
        <button type="button" class="btn" disabled={busy !== null} onClick={() => run('vouch', () => ledger.vouch(fact.id))}>
          {busy === 'vouch' ? 'Confirming…' : 'I checked: still true'}
        </button>
        <button type="button" class="btn" onClick={() => onMode({ kind: 'new', replaces: fact.id })}>
          Give it a check…
        </button>
      </div>
    </section>
  )
}

/** Retracting asks why, and keeps the answer with the fact. */
function Retract({ id, ledger }: { id: string; ledger: Ledger }) {
  const [asking, setAsking] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const field = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (asking) field.current?.focus()
  }, [asking])
  if (!asking) {
    return (
      <section class="part part-end">
        <button type="button" class="link" onClick={() => setAsking(true)}>
          Retract this fact…
        </button>
      </section>
    )
  }
  const send = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await ledger.retract(id, reason.trim())
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }
  return (
    <section class="part part-end">
      <form class="form" onSubmit={send}>
        <label class="field">
          <span>Why is it being retracted?</span>
          <input
            type="text"
            value={reason}
            maxLength={300}
            required
            autocomplete="off"
            placeholder="we moved off that service"
            onInput={(e) => setReason(e.currentTarget.value)}
            ref={field}
          />
          <small>The reason is kept with the fact.</small>
        </label>
        {error && <p class="refusal">{error}</p>}
        <div class="actions">
          <button type="submit" class="btn btn-warn" disabled={busy || reason.trim() === ''}>
            {busy ? 'Retracting…' : 'Retract'}
          </button>
          <button type="button" class="btn" onClick={() => setAsking(false)}>
            Keep it
          </button>
        </div>
      </form>
    </section>
  )
}
