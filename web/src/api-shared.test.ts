import { describe, expect, it } from 'vitest'
import { Refusal, settle, unwrap } from './api-shared'
import { showAs, timedOut } from './derive'
import type { View } from './types'

const view = (id: string, status: string, more: object = {}) => ({ id, claim: id, created: '2026-01-05T09:00:00Z', status, ...more })

describe('settle', () => {
  it('reads the status "timed_out" as a fact that held and was not proved', () => {
    const reply = {
      facts: [
        view('f-aaaaaa', 'timed_out', { late: true, detail: 'localhost:8080 accepts connections' }),
        view('f-bbbbbb', 'holds'),
        view('f-cccccc', 'broken', { late: true }),
        view('f-dddddd', 'blocked', { fix: 'holdfast allow net example.com' }),
      ],
      counts: { total: 4, holds: 1, broken: 1, blocked: 1, stale: 0, unproven: 0, timed_out: 1, late: 2 },
    }
    const got = settle(reply) as unknown as { facts: View[] }
    expect(got.facts.map((v) => v.status)).toEqual(['holds', 'holds', 'broken', 'blocked'])
    expect(got.facts.map((v) => v.late === true)).toEqual([true, false, true, false])
    expect(got.facts.map(timedOut)).toEqual([true, false, false, false])
    expect(got.facts.map(showAs)).toEqual(['late', 'holds', 'broken', 'blocked'])
    // nothing else is touched
    expect(got.facts[0]!.detail).toBe('localhost:8080 accepts connections')
    expect(got.facts[3]!.fix).toBe('holdfast allow net example.com')
  })

  it('marks the fact even when the answer left the mark out', () => {
    const got = settle({ fact: view('f-aaaaaa', 'timed_out') }) as unknown as { fact: View }
    expect(got.fact.status).toBe('holds')
    expect(got.fact.late).toBe(true)
  })

  it('finds a fact wherever it sits in the answer', () => {
    const got = settle({
      state: { facts: [view('f-aaaaaa', 'timed_out')], gone: [view('f-bbbbbb', 'holds')] },
      fact: view('f-aaaaaa', 'timed_out'),
    }) as unknown as { state: { facts: View[]; gone: View[] }; fact: View }
    expect(showAs(got.state.facts[0]!)).toBe('late')
    expect(showAs(got.fact)).toBe('late')
    expect(showAs(got.state.gone[0]!)).toBe('holds')
  })

  it('leaves text alone, whatever it says', () => {
    const fact = view('f-aaaaaa', 'holds', { claim: 'timed_out', detail: 'the page says "status": "timed_out"', tags: ['timed_out'] })
    expect(settle(structuredClone(fact))).toEqual(fact)
    for (const plain of [null, undefined, 0, '', 'timed_out', true, []]) expect(settle(plain)).toEqual(plain)
  })
})

describe('unwrap', () => {
  it('gives the result of a call that worked, settled', () => {
    const got = unwrap<{ facts: View[] }>({ ok: true, result: { facts: [view('f-aaaaaa', 'timed_out', { late: true })] } })
    expect(timedOut(got.facts[0]!)).toBe(true)
  })

  it('throws a refusal as a refusal and a fault as an error', () => {
    expect(() => unwrap({ ok: false, refused: true, error: 'the claim is empty' })).toThrow(Refusal)
    expect(() => unwrap({ ok: false, error: 'the journal could not be read' })).toThrow('the journal could not be read')
    expect(() => unwrap({ ok: false })).toThrow('the engine gave no reason')
    try {
      unwrap({ ok: false, error: 'the journal could not be read' })
    } catch (err) {
      expect(err).not.toBeInstanceOf(Refusal)
    }
  })
})
