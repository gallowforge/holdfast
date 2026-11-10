// The demo's door to the engine: the same engine, compiled to WebAssembly
// (cmd/holdfast-wasm) and running in this tab against a pretend machine.
// Both files sit beside the page.
import { unwrap } from '../api-shared'

export { Refusal } from '../api-shared'

interface GoRuntime {
  importObject: WebAssembly.Imports
  run(instance: WebAssembly.Instance): Promise<void>
}

interface DemoEngine {
  call(method: string, body: string): Promise<string>
  version: string
}

declare global {
  /** Go's loader, wasm_exec.js. */
  var Go: (new () => GoRuntime) | undefined
  /** Set by the engine when it has started. */
  var holdfastDemo: DemoEngine | undefined
}

const beside = (file: string) => new URL(file, document.baseURI).href

function script(src: string): Promise<void> {
  return new Promise((done, fail) => {
    const s = document.createElement('script')
    s.src = src
    s.onload = () => done()
    s.onerror = () => fail(new Error(`${src} did not load`))
    document.head.append(s)
  })
}

async function instantiate(url: string, imports: WebAssembly.Imports): Promise<WebAssembly.Instance> {
  // compiling while it downloads needs the host to name the type; any other host gets the plain way
  if (typeof WebAssembly.instantiateStreaming === 'function') {
    try {
      return (await WebAssembly.instantiateStreaming(fetch(url), imports)).instance
    } catch {
      /* fall through */
    }
  }
  const r = await fetch(url)
  if (!r.ok) throw new Error(`${url}: HTTP ${r.status}`)
  return (await WebAssembly.instantiate(await r.arrayBuffer(), imports)).instance
}

async function start(): Promise<DemoEngine> {
  if (globalThis.holdfastDemo) return globalThis.holdfastDemo
  if (typeof WebAssembly !== 'object') throw new Error('This browser has no WebAssembly, which the demo runs on.')
  if (!globalThis.Go) await script(beside('wasm_exec.js'))
  if (!globalThis.Go) throw new Error('wasm_exec.js did not define Go')
  const go = new globalThis.Go()
  const instance = await instantiate(beside('holdfast.wasm'), go.importObject)
  // run() resolves when the program exits, and this one stays loaded for as
  // long as the page is open; its main has set holdfastDemo by the time it parks
  void go.run(instance)
  for (let i = 0; i < 1000 && !globalThis.holdfastDemo; i++) await new Promise((r) => setTimeout(r, 10))
  if (!globalThis.holdfastDemo) throw new Error('The engine did not start.')
  return globalThis.holdfastDemo
}

let engine: Promise<DemoEngine> | undefined

/** Downloads and starts the engine, once. */
export function ready(): Promise<void> {
  engine ??= start()
  return engine.then(() => undefined)
}

export async function call<T>(method: string, body: object = {}): Promise<T> {
  engine ??= start()
  const e = await engine
  return unwrap<T>(JSON.parse(await e.call(method, JSON.stringify(body))))
}
