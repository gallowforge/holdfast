// The one door to the engine: POST /api/<method> on the server that sent this
// page. The demo site swaps this file for demo/api.ts, which calls the same
// engine compiled for the browser; nothing else in the page knows which it has.
import { unwrap, type Reply } from './api-shared'

export { Refusal } from './api-shared'

/** Nothing to prepare: the server is already there. */
export function ready(): Promise<void> {
  return Promise.resolve()
}

export async function call<T>(method: string, body: object = {}): Promise<T> {
  let r: Response
  try {
    r = await fetch(`/api/${method}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Holdfast': '1' },
      body: JSON.stringify(body),
    })
  } catch {
    throw new Error('The ledger is not answering. Is "holdfast serve" still running?')
  }
  let reply: Reply
  try {
    reply = (await r.json()) as Reply
  } catch {
    throw new Error(`The ledger answered with something unreadable (HTTP ${r.status}).`)
  }
  return unwrap<T>(reply)
}
