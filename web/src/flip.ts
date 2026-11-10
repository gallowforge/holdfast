// Rows glide to where they now belong instead of jumping there: when a fact
// breaks it travels up to "Needs a person", and the eye can follow it.
import type { RefObject } from 'preact'
import { useLayoutEffect, useRef } from 'preact/hooks'

/**
 * Remembers where every [data-flip] row under root sat, and when the signature
 * changes, moves each row from its old place to its new one.
 */
export function useGlide(root: RefObject<HTMLElement | null>, signature: string): void {
  const seen = useRef(new Map<string, number>())
  useLayoutEffect(() => {
    const el = root.current
    if (!el || el.offsetParent === null) {
      seen.current = new Map() // not on screen: nothing to glide from
      return
    }
    // measured against the scrolled content, so scrolling is not mistaken for moving
    const base = el.getBoundingClientRect().top - el.scrollTop
    const rows = [...el.querySelectorAll<HTMLElement>('[data-flip]')]
    const next = new Map(rows.map((row) => [row.dataset.flip!, row.getBoundingClientRect().top - base]))
    const still = matchMedia('(prefers-reduced-motion: reduce)').matches || typeof el.animate !== 'function'
    if (!still) {
      for (const row of rows) {
        const was = seen.current.get(row.dataset.flip!)
        if (was === undefined) continue
        const dy = was - next.get(row.dataset.flip!)!
        if (Math.abs(dy) < 1) continue
        row.animate([{ transform: `translateY(${dy}px)` }, { transform: 'none' }], { duration: 460, easing: 'cubic-bezier(0.2, 0.75, 0.2, 1)' })
      }
    }
    seen.current = next
  }, [signature])
}
