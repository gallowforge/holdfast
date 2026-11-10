// The envelope every answer travels in, whichever door it came through.

/** The engine understood the call and said no, in words meant for a person. */
export class Refusal extends Error {}

export interface Reply {
  ok: boolean
  result?: unknown
  error?: string
  refused?: boolean
}

/**
 * The engine writes a fact whose check did not finish, and which held when it
 * last ran, with the status "timed_out", so that nobody reading the answer
 * finds "holds" on a fact that was not proved. The page keeps what the fact
 * last was and the mark beside it, as the engine itself does.
 */
export function settle<T>(value: T): T {
  if (Array.isArray(value)) {
    for (const item of value) settle(item)
  } else if (value !== null && typeof value === 'object') {
    const o = value as Record<string, unknown>
    for (const key of Object.keys(o)) settle(o[key])
    if (o.status === 'timed_out') {
      o.status = 'holds'
      o.late = true
    }
  }
  return value
}

export function unwrap<T>(reply: Reply): T {
  if (reply.ok) return settle(reply.result) as T
  const why = reply.error || 'the engine gave no reason'
  throw reply.refused ? new Refusal(why) : new Error(why)
}
