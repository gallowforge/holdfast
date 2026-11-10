// Small pieces more than one part of the page uses.
import type { ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './glyphs'

/** Copies a line of text, and says so. */
export function Copy({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setDone(true)
      clearTimeout(timer.current)
      timer.current = setTimeout(() => setDone(false), 1400)
    } catch {
      /* the browser refused: the text is on screen to select by hand */
    }
  }
  return (
    <button type="button" class="copy" onClick={copy} aria-label={done ? 'Copied' : `${label}: ${text}`} title={done ? 'Copied' : label}>
      <Icon name={done ? 'check' : 'copy'} size={14} />
    </button>
  )
}

/** A line to type into a terminal, with a way to copy it. */
export function Command({ text }: { text: string }) {
  return (
    <span class="command">
      <code>{text}</code>
      <Copy text={text} />
    </span>
  )
}

/** The head every pane opens with: what it is, and a way back. */
export function PaneHead({ title, onClose, children }: { title: string; onClose?: () => void; children?: ComponentChildren }) {
  return (
    <header class="pane-head">
      <div class="pane-title">
        <h2>{title}</h2>
        {children}
      </div>
      {onClose && (
        <button type="button" class="icon-btn pane-close" onClick={onClose} aria-label="Close" title="Close (Esc)">
          <Icon name="close" />
        </button>
      )}
    </header>
  )
}
