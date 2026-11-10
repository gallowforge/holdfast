// The form for a new fact. A claim with a check is a fact; a claim without
// one is kept as a note, and the form says so before it is saved.
import { useEffect, useRef, useState } from 'preact/hooks'
import { PaneHead } from './bits'
import { Icon } from './glyphs'
import { GRAMMAR, GRAMMAR_NOTE } from './grammar'
import { parse, type Draft, type Ledger } from './ledger'
import type { Parsed, View } from './types'

interface Props {
  /** The fact this one takes the place of, if any. */
  replaces?: View
  ledger: Ledger
  onClose(): void
  /** Drops "replaces": the new fact is stored beside the old one. */
  onKeepAlone(): void
  onDone(id: string): void
}

const tagsOf = (text: string): string[] => [
  ...new Set(
    text
      .toLowerCase()
      .split(/[\s,]+/)
      .map((t) => t.replace(/^#/, ''))
      .filter(Boolean),
  ),
]

export function NewPane({ replaces, ledger, onClose, onKeepAlone, onDone }: Props) {
  const [claim, setClaim] = useState(replaces?.claim ?? '')
  const [check, setCheck] = useState(replaces?.check ?? '')
  const [tags, setTags] = useState((replaces?.tags ?? []).join(' '))
  const [ttl, setTtl] = useState('')
  const [parsed, setParsed] = useState<Parsed | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const claimField = useRef<HTMLTextAreaElement>(null)
  const checkField = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const el = claimField.current
    if (!el) return
    el.focus()
    el.setSelectionRange(el.value.length, el.value.length)
  }, [])

  // The engine reads the check while it is typed; the page does not guess.
  const typedCheck = check.trim()
  useEffect(() => {
    if (typedCheck === '') {
      setParsed(null)
      return
    }
    let live = true
    const wait = setTimeout(() => {
      parse(typedCheck).then(
        (p) => live && setParsed(p),
        () => live && setParsed(null),
      )
    }, 200)
    return () => {
      live = false
      clearTimeout(wait)
    }
  }, [typedCheck])

  const send = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    const draft: Draft = { claim: claim.trim(), check: typedCheck, tags: tagsOf(tags) }
    if (replaces) draft.replaces = replaces.id
    if (typedCheck === '' && ttl.trim() !== '') draft.ttl = ttl.trim()
    try {
      const { fact } = await ledger.remember(draft)
      onDone(fact.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }
  const start = (form: string) => {
    setCheck(form.split(' ')[0]! + ' ')
    checkField.current?.focus()
  }
  const malformed = typedCheck !== '' && parsed !== null && !parsed.ok

  return (
    <>
      <PaneHead title={replaces ? 'Replace a fact' : 'Add a fact'} onClose={onClose}>
        <p>A claim, and the check that proves it.</p>
      </PaneHead>
      <form class="pane-body form" onSubmit={send}>
        {replaces && (
          <p class="replacing">
            <span>
              Takes the place of <code>{replaces.id}</code> {replaces.claim}
            </span>
            <button type="button" class="link" onClick={onKeepAlone}>
              Keep both instead
            </button>
          </p>
        )}

        <label class="field">
          <span>Claim</span>
          <textarea
            ref={claimField}
            value={claim}
            rows={3}
            maxLength={400}
            required
            placeholder="The API listens on port 8080"
            onInput={(e) => setClaim(e.currentTarget.value)}
          />
        </label>

        <label class="field">
          <span>Check</span>
          <input
            ref={checkField}
            class="mono"
            type="text"
            value={check}
            maxLength={1000}
            autocomplete="off"
            autocapitalize="off"
            spellcheck={false}
            placeholder="port localhost:8080"
            aria-invalid={malformed}
            onInput={(e) => setCheck(e.currentTarget.value)}
          />
          {typedCheck === '' ? (
            <small>With no check it is kept as a note. Nothing proves a note, and it goes stale.</small>
          ) : malformed ? (
            <small class="bad">{parsed!.error}</small>
          ) : parsed?.ok ? (
            <small class="good">
              <Icon name="check" size={12} />
              Well formed. It runs when you save, and a claim that fails is not stored.
            </small>
          ) : (
            <small>&nbsp;</small>
          )}
        </label>

        <details class="grammar">
          <summary>What a check can say</summary>
          <ul>
            {GRAMMAR.map(([form, means]) => (
              <li key={form}>
                <button type="button" onClick={() => start(form)} title="Start a check of this kind">
                  <code>{form}</code>
                  <span>{means}</span>
                </button>
              </li>
            ))}
          </ul>
          <p>{GRAMMAR_NOTE}</p>
        </details>

        {typedCheck === '' && (
          <label class="field field-short">
            <span>Goes stale after</span>
            <input
              class="mono"
              type="text"
              value={ttl}
              maxLength={12}
              autocomplete="off"
              spellcheck={false}
              placeholder="30d"
              onInput={(e) => setTtl(e.currentTarget.value)}
            />
            <small>A length of time such as 12h, 7d or 4w.</small>
          </label>
        )}

        <label class="field">
          <span>Tags</span>
          <input
            type="text"
            value={tags}
            maxLength={200}
            autocomplete="off"
            autocapitalize="off"
            spellcheck={false}
            placeholder="api deploy"
            onInput={(e) => setTags(e.currentTarget.value)}
          />
        </label>

        {error && <p class="refusal">{error}</p>}

        <div class="actions">
          <button type="submit" class="btn btn-primary" disabled={busy || claim.trim() === '' || malformed}>
            {busy ? 'Proving…' : typedCheck === '' ? 'Keep as a note' : 'Prove and remember'}
          </button>
          <button type="button" class="btn" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </>
  )
}
