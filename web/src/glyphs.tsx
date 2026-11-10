// The statuses, told apart by shape as well as colour, and the few icons
// the page needs. All drawn here: no icon font, no image requests.
import type { Shown } from './types'

export type Tone = Shown | 'gone'

export function Glyph({ status, size = 16, title }: { status: Tone; size?: number; title?: string }) {
  return (
    <svg class={`glyph t-${status}`} width={size} height={size} viewBox="0 0 16 16" role={title ? 'img' : undefined} aria-label={title} aria-hidden={title ? undefined : 'true'}>
      {shape(status)}
    </svg>
  )
}

function shape(status: Tone) {
  switch (status) {
    case 'holds': // a solid dot: settled
      return <circle cx="8" cy="8" r="4.25" fill="currentColor" />
    case 'broken': // a cross: no longer so
      return <path d="M4.6 4.6l6.8 6.8m0-6.8l-6.8 6.8" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
    case 'blocked': // a barred ring: not allowed to look
      return (
        <>
          <circle cx="8" cy="8" r="5.2" fill="none" stroke="currentColor" stroke-width="1.6" />
          <path d="M5.3 8h5.4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
        </>
      )
    case 'late': // a clock face: the check ran out of time
      return (
        <>
          <circle cx="8" cy="8" r="5.2" fill="none" stroke="currentColor" stroke-width="1.5" />
          <path d="M8 5.1V8l1.9 1.4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </>
      )
    case 'stale': // a half-spent disc: past its life
      return (
        <>
          <circle cx="8" cy="8" r="5.2" fill="none" stroke="currentColor" stroke-width="1.5" />
          <path d="M8 2.8a5.2 5.2 0 0 0 0 10.4z" fill="currentColor" />
        </>
      )
    case 'unproven': // a dotted ring: nothing inside it yet
      return <circle cx="8" cy="8" r="5.2" fill="none" stroke="currentColor" stroke-width="1.7" stroke-dasharray="0.01 2.7127" stroke-linecap="round" />
    default: // a dash: withdrawn
      return <path d="M4.5 8h7" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
  }
}

/** The mark: a holdfast, the root a kelp grips the rock with. */
export function Mark({ size = 20 }: { size?: number }) {
  return (
    <svg class="mark" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M12 2.5V10" />
      <path d="M12 10c0 4-5 3.6-6.4 8.2" />
      <path d="M12 10v6.6" />
      <path d="M12 10c0 4 5 3.6 6.4 8.2" />
      <path d="M3 21.5c3.2-4.6 14.8-4.6 18 0" />
    </svg>
  )
}

const ICONS = {
  close: 'M4.5 4.5l7 7m0-7l-7 7',
  plus: 'M8 3.5v9M3.5 8h9',
  search: 'M7 11.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9zm3.3-1.2l3.2 3.2',
  copy: 'M6 6V3.5h6.5V10H10M3.5 6H10v6.5H3.5z',
  check: 'M3.5 8.5l3 3 6-7',
  out: 'M6.5 4.5h5v5m0-5l-7 7',
  back: 'M9.5 3.5L5 8l4.5 4.5',
  undo: 'M5.5 3.5L3 6l2.5 2.5M3 6h6a3.5 3.5 0 0 1 0 7H6.5',
  again: 'M12.5 8a4.5 4.5 0 1 1-1.5-3.4M12.5 3v2.5H10',
  sun: 'M8 10.6a2.6 2.6 0 1 0 0-5.2 2.6 2.6 0 0 0 0 5.2zM8 1.8v1.4m0 9.6v1.4M1.8 8h1.4m9.6 0h1.4M3.6 3.6l1 1m6.8 6.8l1 1m0-8.8l-1 1M4.6 11.4l-1 1',
  moon: 'M13 9.6A5.4 5.4 0 0 1 6.4 3 5.4 5.4 0 1 0 13 9.6z',
  system: 'M2.5 3.5h11v7h-11zM6 13h4M8 10.5V13',
} as const

export type IconName = keyof typeof ICONS

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg class="icon" width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d={ICONS[name]} />
    </svg>
  )
}
