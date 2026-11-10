import { readFileSync } from 'node:fs'
import { expect, test } from 'vitest'
import { GRAMMAR, GRAMMAR_NOTE } from './grammar'

test('the grammar shown in the page is the engine’s own, word for word', () => {
  const go = readFileSync(new URL('../../internal/fact/check.go', import.meta.url), 'utf8')
  const found = /const Grammar = `([^`]+)`/.exec(go)
  expect(found, 'check.go no longer declares Grammar as one raw string').not.toBeNull()
  const mine = [...GRAMMAR.map(([form, means]) => `${form}  ${means}`), GRAMMAR_NOTE]
  const theirs = found![1]!.split('\n').map((line) => line.replace(/\s{2,}/, '  '))
  expect(mine).toEqual(theirs)
})
