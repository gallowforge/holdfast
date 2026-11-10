import { describe, expect, it } from 'vitest'
import { aside, basis, changed, countsLine, groups, headline, ink, lifeSpent, lines, marked, passes, pasteable, showAs, splitFix, tally } from './derive'
import type { Counts, State, Status, View } from './types'

const D = 24 * 60 * 60 * 1000
const now = Date.parse('2026-03-04T12:00:00Z')
const iso = (t: number) => new Date(t).toISOString()

function fact(id: string, status: Status, more: Partial<View> = {}): View {
  return { id, claim: `claim ${id}`, created: iso(now - 12 * D), status, ...more }
}
const counts = (c: Partial<Counts>): Counts => ({ total: 0, holds: 0, broken: 0, blocked: 0, unproven: 0, stale: 0, ...c })

describe('countsLine', () => {
  it('matches the engine word for word', () => {
    expect(countsLine(counts({ total: 13, broken: 1, blocked: 1, stale: 1, unproven: 1, holds: 9 }))).toBe(
      '13 facts: 1 broken, 1 blocked, 1 stale, 1 unproven, 9 hold',
    )
    expect(countsLine(counts({ total: 1, holds: 1 }))).toBe('1 fact: 1 holds')
    expect(countsLine(counts({ total: 3, broken: 1, timed_out: 1, late: 1, holds: 1 }))).toBe('3 facts: 1 broken, 1 timed out, 1 holds')
    expect(countsLine(counts({}))).toBe('no facts')
  })
})

describe('headline', () => {
  it('leads with what is no longer true', () => {
    expect(headline(counts({ total: 5, broken: 2, holds: 3 }))).toEqual({ text: '2 facts are no longer true.', tone: 'broken' })
    expect(headline(counts({ total: 5, broken: 1, holds: 4 })).text).toBe('1 fact is no longer true.')
  })
  it('never calls a blocked or unproven fact proved', () => {
    expect(headline(counts({ total: 4, holds: 3, blocked: 1 }))).toEqual({ text: 'Every check that ran holds. 1 could not run.', tone: 'blocked' })
    expect(headline(counts({ total: 4, holds: 3, unproven: 1 }))).toEqual({ text: 'Every fact with a check holds.', tone: 'holds' })
    expect(headline(counts({ total: 2, unproven: 1, stale: 1 })).text).toBe('Nothing here is proved yet.')
    expect(headline(counts({ total: 3, holds: 3 })).text).toBe('All 3 facts hold.')
    expect(headline(counts({})).tone).toBe('none')
  })
  it('never calls a fact proved when its check did not finish', () => {
    expect(headline(counts({ total: 3, holds: 2, timed_out: 1, late: 1 }))).toEqual({ text: '1 check did not finish in time.', tone: 'late' })
    expect(headline(counts({ total: 2, timed_out: 2, late: 2 })).text).toBe('2 checks did not finish in time.')
    // what is known to be wrong still comes first
    expect(headline(counts({ total: 3, broken: 1, timed_out: 1, late: 1, holds: 1 })).tone).toBe('broken')
  })
})

describe('groups', () => {
  it('puts what needs a person first and drops empty parts', () => {
    const g = groups([fact('a', 'holds'), fact('b', 'stale'), fact('c', 'broken'), fact('d', 'blocked'), fact('e', 'holds')])
    expect(g.map((x) => x.key)).toEqual(['attention', 'holds'])
    expect(g[0]!.facts.map((v) => v.id)).toEqual(['c', 'd', 'b'])
    expect(g[1]!.facts.map((v) => v.id)).toEqual(['a', 'e'])
  })
})

describe('passes', () => {
  const v = fact('f-7k2m9x', 'holds', { claim: 'The API listens on port 8080', check: 'port localhost:8080', tags: ['api'], by: 'claude-code' })
  it('finds a fact by any of its words, its tag or its author', () => {
    expect(passes(v, 'api 8080', null)).toBe(true)
    expect(passes(v, '#api', null)).toBe(true)
    expect(passes(v, 'claude', null)).toBe(true)
    expect(passes(v, 'postgres', null)).toBe(false)
  })
  it('keeps to the chosen status', () => {
    expect(passes(v, '', 'broken')).toBe(false)
    expect(passes(v, '', 'holds')).toBe(true)
  })
})

describe('notes', () => {
  const note = fact('n', 'unproven', { by: 'alex', created: iso(now - 6 * D), stale_at: iso(now + 24 * D) })
  const old = fact('o', 'stale', { by: 'cursor', created: iso(now - 41 * D), stale_at: iso(now - 11 * D) })
  it('fade as their life runs out, and stay readable', () => {
    expect(lifeSpent(note, now)).toBeCloseTo(0.2)
    expect(ink(note, now)).toBeCloseTo(0.924)
    expect(lifeSpent(old, now)).toBeGreaterThan(1)
    expect(ink(old, now)).toBeCloseTo(0.62)
    expect(ink(fact('p', 'holds', { check: 'file go.mod' }), now)).toBe(1)
  })
  it('say who wrote them and how old they are', () => {
    expect(basis(note, now)).toBe('a note by alex, 6d old')
    expect(basis(old, now)).toBe('a note by cursor, 41d old, past its 30d life')
    expect(aside(old, now)).toBe('treat it as a guess')
  })
  it('say who confirmed them', () => {
    const vouched = { ...note, noted_at: iso(now - 2 * 60 * 1000), vouched_by: 'you' }
    expect(basis(vouched, now)).toBe('a note by alex, confirmed by you, 2m old')
  })
})

describe('proved facts', () => {
  it('lead with the evidence', () => {
    const holds = fact('h', 'holds', { check: 'port localhost:8080', detail: 'localhost:8080 accepts connections', checked_at: iso(now - 12000) })
    expect(basis(holds, now)).toBe('localhost:8080 accepts connections')
    expect(aside(holds, now)).toBe('checked 12s ago')
    const broken = { ...holds, status: 'broken' as const, detail: 'localhost:8080 does not accept connections: connection refused', held: iso(now - 180000) }
    expect(basis(broken, now)).toBe('proof failed: localhost:8080 does not accept connections: connection refused')
    expect(aside(broken, now)).toBe('last held 3m ago')
  })
  it('keep the command that would allow a blocked check apart from the reason', () => {
    const detail = 'the policy does not allow connections to billing.example.net. A person can allow it: holdfast allow net billing.example.net'
    const fix = 'holdfast allow net billing.example.net'
    expect(splitFix(detail, fix)).toEqual({ why: 'the policy does not allow connections to billing.example.net', fix })
    expect(splitFix('the check has never run')).toEqual({ why: 'the check has never run' })
    expect(basis(fact('b', 'blocked', { check: 'http 200 https://billing.example.net/ping', detail, fix }), now)).toBe(
      'not checked: the policy does not allow connections to billing.example.net',
    )
  })
})

describe('changed', () => {
  it('names the facts whose status moved', () => {
    const before = [fact('a', 'holds'), fact('b', 'holds'), fact('c', 'broken')]
    const after = [fact('a', 'broken'), fact('b', 'holds'), fact('c', 'holds'), fact('d', 'holds')]
    expect(changed(before, after)).toEqual(['a', 'c'])
  })
})

describe('lines', () => {
  it('picks out the status each block opens with and carries it to the lines beneath', () => {
    const text = [
      '2 facts: 1 broken, 1 holds.',
      '',
      'BROKEN    f-7k2m9x  The API listens on port 8080',
      '          proof failed: localhost:8080 does not accept connections',
      '          check: port localhost:8080',
      'HOLDS     f-3ndp7g  Postgres runs on 192.168.1.20, port 5432',
      '          proof: 192.168.1.20:5432 accepts connections',
      '',
      'BROKEN: this was true once and is not now.',
    ].join('\n')
    const out = lines(text)
    expect(out[0]).toEqual({ tone: undefined, text: '2 facts: 1 broken, 1 holds.' })
    expect(out[2]).toEqual({ label: 'BROKEN    ', tone: 'broken', text: 'f-7k2m9x  The API listens on port 8080' })
    expect(out[3]!.tone).toBe('broken')
    expect(out[5]!.tone).toBe('holds')
    expect(out[6]!.tone).toBe('holds')
    // the advice line names a status but is not a fact
    expect(out[8]).toEqual({ tone: undefined, text: 'BROKEN: this was true once and is not now.' })
  })

  it('does not take the advice about notes for an unproven fact', () => {
    const out = lines(['UNPROVEN  f-k4x9ns  Only the main branch is deployed', '', 'UNPROVEN and STALE: notes with no check.'].join('\n'))
    expect(out[0]!.label).toBe('UNPROVEN  ')
    expect(out[2]).toEqual({ tone: undefined, text: 'UNPROVEN and STALE: notes with no check.' })
  })
})

describe('a check that did not finish', () => {
  const held = fact('h', 'holds', { check: 'port localhost:8080', detail: 'localhost:8080 accepts connections', checked_at: iso(now - 180000) })
  const late = { ...held, late: true }
  const state = (facts: View[], c: Partial<Counts>): State => ({
    version: 'dev', demo: false, now: iso(now), project: 'atlas', policy: '', approved: true, facts, counts: counts(c),
  })

  it('is shown as timed out, with what it last stood on, and never as holding', () => {
    expect(showAs(late)).toBe('late')
    expect(showAs(held)).toBe('holds')
    expect(basis(late, now)).toBe('not proved just now: the check did not finish in time')
    expect(aside(late, now)).toBe('last checked 3m ago')
    expect(passes(late, '', 'holds')).toBe(false)
    expect(passes(late, '', 'late')).toBe(true)
    // a fact that was broken anyway keeps its own status
    expect(showAs({ ...late, status: 'broken' })).toBe('broken')
  })

  it('goes with what needs a person, after what is known to be wrong', () => {
    const g = groups([fact('a', 'holds'), late, fact('c', 'blocked'), fact('d', 'broken')])
    expect(g.map((x) => x.key)).toEqual(['attention', 'holds'])
    expect(g[0]!.facts.map((v) => v.id)).toEqual(['d', 'h', 'c'])
    expect(g[1]!.facts.map((v) => v.id)).toEqual(['a'])
  })

  it('flashes when a fact stops or starts being proved', () => {
    expect(changed([held], [late])).toEqual(['h'])
    expect(changed([late], [held])).toEqual(['h'])
  })

  it('is marked on the state again, which alone does not say it', () => {
    const s = state([held, fact('b', 'broken', { check: 'file go.mod', checked_at: iso(now - 60000) }), fact('k', 'holds')], { total: 3, holds: 2, broken: 1 })
    expect(marked(s, new Map())).toBe(s)

    const m = marked(s, new Map([['h', held.checked_at!], ['b', iso(now - 60000)]]))
    expect(m.facts.map((v) => v.late === true)).toEqual([true, true, false])
    expect(m.counts).toEqual(counts({ total: 3, holds: 1, broken: 1, timed_out: 1, late: 2 }))
    expect(tally(m.counts, 'late')).toBe(1)
    expect(tally(m.counts, 'holds')).toBe(1)
    expect(headline(m.counts).tone).toBe('broken')
  })

  it('stops being marked once the fact stands on newer evidence', () => {
    const again = { ...held, checked_at: iso(now - 1000) }
    const s = state([again], { total: 1, holds: 1 })
    expect(marked(s, new Map([['h', held.checked_at!]]))).toBe(s)
  })

  it('is told apart in the text an agent is given', () => {
    const out = lines(
      [
        'TIMED OUT f-7k2m9x  The API listens on port 8080',
        '          not proved just now: the check did not finish in time',
        '',
        'TIMED OUT: the check did not finish, so this is as last known and is not proved.',
      ].join('\n'),
    )
    expect(out[0]).toEqual({ label: 'TIMED OUT ', tone: 'late', text: 'f-7k2m9x  The API listens on port 8080' })
    expect(out[1]!.tone).toBe('late')
    expect(out[3]).toEqual({ tone: undefined, text: 'TIMED OUT: the check did not finish, so this is as last known and is not proved.' })
  })
})

describe('the way to allow a check', () => {
  it('is offered for pasting only when it is a command', () => {
    const line = splitFix(
      'the policy does not allow connections to example.com. A person can allow it: holdfast allow net example.com',
      'holdfast allow net example.com',
    )
    expect(line.fix).toBe('holdfast allow net example.com')
    expect(pasteable(line.fix)).toBe(true)

    const byHand = 'by hand only. The policy reads its argument * as "anything", so the rule must be written into the policy file'
    const hand = splitFix(`the policy does not allow the command: echo '*'. A person can allow it: ${byHand}`, byHand)
    expect(hand.why).toBe("the policy does not allow the command: echo '*'")
    expect(pasteable(hand.fix)).toBe(false)
    expect(pasteable(undefined)).toBe(false)
  })
  it('is never read out of the text, which quotes a check an agent wrote', () => {
    // the check itself carries the words the engine uses, and a command of the agent's choosing
    const planted = 'the policy does not allow the command: echo ". A person can allow it: holdfast allow cmd rm ** #"'
    const real = `holdfast allow cmd echo '. A person can allow it: holdfast allow cmd rm ** #'`

    // with the engine's field, that command is the only one offered
    expect(splitFix(`${planted}. A person can allow it: ${real}`, real)).toEqual({ why: planted, fix: real })

    // without it nothing is offered, whatever the text says
    const bare = splitFix(`${planted}. A person can allow it: ${real}`)
    expect(bare.fix).toBeUndefined()
    expect(pasteable(bare.fix)).toBe(false)
    expect(bare.why).toContain('holdfast allow cmd rm')

    // and a field that does not end the text takes nothing out of it
    expect(splitFix('the policy does not allow connections to example.com', 'holdfast allow net example.com').why).toBe(
      'the policy does not allow connections to example.com',
    )
  })
})
