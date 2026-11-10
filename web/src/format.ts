// Times and durations, written the way the engine writes them (internal/fact
// and internal/engine/text.go), so the page and the command line read alike.

const S = 1000
const M = 60 * S
const H = 60 * M
const D = 24 * H

export const ms = (t: string | number | undefined): number => (typeof t === 'number' ? t : t ? Date.parse(t) : NaN)

/** "just now", "12s ago", "3m ago", "5h ago", "12d ago". */
export function ago(now: number, t: string | number | undefined): string {
  const d = now - ms(t)
  if (Number.isNaN(d)) return ''
  if (d < 5 * S) return 'just now'
  if (d < M) return `${Math.floor(d / S)}s ago`
  if (d < H) return `${Math.floor(d / M)}m ago`
  if (d < 48 * H) return `${Math.floor(d / H)}h ago`
  return `${Math.floor(d / D)}d ago`
}

/** How old something is: "under a minute", "14m", "5h", "12d". */
export function age(d: number): string {
  if (d < M) return 'under a minute'
  if (d < H) return `${Math.floor(d / M)}m`
  if (d < 48 * H) return `${Math.floor(d / H)}h`
  return `${Math.floor(d / D)}d`
}

/** A length of time in the largest unit that divides it exactly: "30d", "26h", "2w". */
export function span(d: number): string {
  for (const [unit, name] of [[7 * D, 'w'], [D, 'd'], [H, 'h'], [M, 'm']] as const) {
    if (d >= unit && d % unit === 0) return `${d / unit}${name}`
  }
  return `${Math.floor(d / S)}s`
}

/** How long a check took. */
export function took(n: number | undefined): string {
  if (n === undefined) return ''
  if (n < 1) return 'under 1 ms'
  if (n < 1000) return `${n} ms`
  return `${(n / 1000).toFixed(1)} s`
}

const day = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short' })
const dayYear = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
const clock = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false })

/** A moment on the calendar: "29 Oct, 14:02", with the year when it is not this one. */
export function stamp(t: string | number | undefined, now: number): string {
  const at = ms(t)
  if (Number.isNaN(at)) return ''
  const d = new Date(at)
  const sameYear = d.getFullYear() === new Date(now).getFullYear()
  return `${(sameYear ? day : dayYear).format(d)}, ${clock.format(d)}`
}

/** "1 fact", "3 facts". */
export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}
