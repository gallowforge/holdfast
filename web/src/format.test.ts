import { describe, expect, it } from 'vitest'
import { age, ago, plural, span, stamp, took } from './format'

const S = 1000
const M = 60 * S
const H = 60 * M
const D = 24 * H
const now = Date.parse('2026-03-04T12:00:00Z')

describe('ago', () => {
  it('reads like the command line', () => {
    expect(ago(now, now - 2 * S)).toBe('just now')
    expect(ago(now, now - 12 * S)).toBe('12s ago')
    expect(ago(now, now - 3 * M)).toBe('3m ago')
    expect(ago(now, now - 47 * H)).toBe('47h ago')
    expect(ago(now, now - 48 * H)).toBe('2d ago')
    expect(ago(now, new Date(now - 12 * D).toISOString())).toBe('12d ago')
  })
  it('calls a moment in the future just now, and says nothing of no moment at all', () => {
    expect(ago(now, now + 9 * S)).toBe('just now')
    expect(ago(now, undefined)).toBe('')
  })
})

describe('age and span', () => {
  it('says how old a note is', () => {
    expect(age(20 * S)).toBe('under a minute')
    expect(age(14 * M)).toBe('14m')
    expect(age(30 * H)).toBe('30h')
    expect(age(41 * D)).toBe('41d')
  })
  it('writes a time to live in its largest exact unit', () => {
    expect(span(30 * D)).toBe('30d')
    expect(span(14 * D)).toBe('2w')
    expect(span(26 * H)).toBe('26h')
    expect(span(90 * S)).toBe('90s')
  })
})

describe('took, stamp, plural', () => {
  it('writes how long a check took', () => {
    expect(took(0)).toBe('under 1 ms')
    expect(took(42)).toBe('42 ms')
    expect(took(1250)).toBe('1.3 s')
    expect(took(undefined)).toBe('')
  })
  it('leaves the year out when it is this one', () => {
    expect(stamp('2026-02-01T09:05:00', now)).toBe('1 Feb, 09:05')
    expect(stamp('2025-02-01T09:05:00', now)).toBe('1 Feb 2025, 09:05')
  })
  it('counts in words', () => {
    expect(plural(1, 'fact')).toBe('1 fact')
    expect(plural(3, 'fact')).toBe('3 facts')
    expect(plural(2, 'check that fails', 'checks that fail')).toBe('2 checks that fail')
  })
})
