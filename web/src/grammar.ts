// What a check can be: a copy of fact.Grammar in internal/fact/check.go, which
// a test keeps word for word in step with the engine.
export const GRAMMAR: readonly (readonly [form: string, means: string])[] = [
  ['file <path>', 'the path exists'],
  ['contains <path> <text>', 'the file contains the text'],
  ['matches <path> <regex>', 'the file matches the regular expression (RE2)'],
  ['unchanged <path>', 'the file is exactly what it was when the fact was stored'],
  ['fresh <path> <duration>', 'the file was modified within the duration (90s, 26h, 7d)'],
  ['http <status> <url> [<text>]', 'a GET answers with the status (and the body contains the text)'],
  ['port <host:port>', 'the port accepts a TCP connection'],
  ['dns <name> [<ip>]', 'the name resolves (and one of its addresses is the ip)'],
  ['cmd <program> [args...]', 'the command exits 0 (only commands a person has allowed)'],
  ['says <text> <program> [args...]', 'the command exits 0 and its output contains the text'],
]

export const GRAMMAR_NOTE = 'Start any check with "not" to assert the opposite. Quote arguments that contain spaces.'
