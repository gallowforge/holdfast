// The README's pictures. Every one is taken from the real thing:
//   - the screens are the page that ships inside the binary, served by
//     `holdfast serve --demo` and driven in a headless browser;
//   - the terminal pictures are the binary's real output, from sessions run
//     here in a throwaway folder, set in the page's own type and colours.
//
//   node shots.mjs <path to the holdfast binary>
//
// It writes ../<name>-{dark,light}.webp, ../<name>-{dark,light}-phone.webp and
// ../social.png. The browser is Playwright's Chromium (`npx playwright install
// chromium`), or an installed Chrome with SHOTS_CHANNEL=chrome.
import { spawn, spawnSync, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { createRequire } from 'node:module'

const { chromium } = createRequire(import.meta.url)('playwright')

const here = import.meta.dirname
const out = path.resolve(here, '..')
const bin = path.resolve(process.argv[2] ?? path.join(here, '../../../bin/holdfast'))
if (!fs.existsSync(bin)) {
  console.error(`shots: there is no binary at ${bin}; run "make build" first`)
  process.exit(2)
}
const only = process.env.ONLY ? process.env.ONLY.split(',') : null
const numbers = JSON.parse(fs.readFileSync(path.join(here, 'numbers.json'), 'utf8')).figures

const THEMES = ['dark', 'light']
// A README column is 840 CSS pixels wide, so wide pictures are 1680 across.
// Phones get their own pictures, taken from the phone layout at 3x.
const WIDE = 1680
const PHONE = { width: 390, height: 800, dpr: 3 }
const QUALITY = Number(process.env.QUALITY ?? 0.9) // WebP; 1 is lossless and four times the size

// ---- the demo server ----

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer()
    s.once('error', reject)
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address()
      s.close(() => resolve(port))
    })
  })
}

/** A fresh demo for each picture, so every one starts from the same machine. */
async function demo() {
  const port = await freePort()
  const proc = spawn(bin, ['serve', '--demo', '--addr', `127.0.0.1:${port}`], { stdio: 'ignore' })
  const url = `http://127.0.0.1:${port}/`
  for (let i = 0; ; i++) {
    try {
      if ((await fetch(url)).ok) break
    } catch {}
    if (i > 100) throw new Error('the demo did not come up')
    await new Promise((r) => setTimeout(r, 50))
  }
  return { url, stop: () => proc.kill('SIGINT') }
}

// ---- pictures ----

let browser
let bench // a blank page that frames a capture and encodes it

/** Round the corners, draw the hairline, and encode. */
async function finish(png, { line, type = 'image/webp' }) {
  const b64 = await bench.evaluate(
    async ({ b64, line, type, quality }) => {
      const img = new Image()
      img.src = 'data:image/png;base64,' + b64
      await img.decode()
      const c = document.createElement('canvas')
      c.width = img.width
      c.height = img.height
      const g = c.getContext('2d')
      const r = Math.round(img.width / 140)
      const frame = () => {
        g.beginPath()
        g.roundRect(0, 0, c.width, c.height, r)
      }
      frame()
      g.clip()
      g.drawImage(img, 0, 0)
      frame()
      g.lineWidth = Math.max(2, Math.round(img.width / 420)) // half of it falls outside the clip
      g.strokeStyle = line
      g.stroke()
      return c.toDataURL(type, quality).split(',')[1]
    },
    { b64: png.toString('base64'), line, type, quality: QUALITY },
  )
  return Buffer.from(b64, 'base64')
}

async function open(server, { width, height, dpr, theme, phone = false }) {
  const ctx = await browser.newContext({
    viewport: { width, height },
    deviceScaleFactor: dpr,
    colorScheme: theme,
    isMobile: phone,
    hasTouch: phone,
    reducedMotion: 'reduce',
  })
  const page = await ctx.newPage()
  await page.goto(server.url)
  await page.waitForSelector('.row')
  await page.evaluate(() => document.fonts.ready)
  return page
}

const line = (page) => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--line').trim())
const settle = (page, ms = 900) => page.waitForTimeout(ms)

/** Flip switches on the pretend machine, by the words on them. */
async function flip(page, ...labels) {
  for (const label of labels) {
    await page.locator('.switch button', { hasText: label }).first().click()
    await page.waitForFunction(() => !document.querySelector('.switch.is-busy'))
    await settle(page, 700)
  }
}
const tab = (page, n) => page.click(`.tabs button:nth-child(${n})`)
const row = (page, text) => page.locator('.row', { hasText: text }).first()

async function save(name, theme, phone, buf) {
  const file = path.join(out, `${name}-${theme}${phone ? '-phone' : ''}.webp`)
  fs.writeFileSync(file, buf)
  console.log(`${path.basename(file).padEnd(30)} ${(buf.length / 1024).toFixed(0).padStart(5)} KB`)
}

// ---- the screens ----

// Each screen: how to get the demo into the state worth showing, and which
// columns of the page to keep. `tall` is the most the picture may be; the
// window is then cut to the last whole row of the ledger, so no row is halved.
const SCREENS = [
  {
    // The whole page, after two things changed behind the agent's back.
    name: 'hero',
    keep: ['.bar', '.cols'],
    tall: 910,
    phoneTall: 820,
    wide: (page) => flip(page, 'Stop the API', 'Upgrade Go to 1.25'),
    phone: async (page) => {
      await tab(page, 2)
      await flip(page, 'Stop the API', 'Upgrade Go to 1.25')
      await tab(page, 1)
    },
  },
  {
    // What the agent is told: the notes file's answer beside Holdfast's.
    name: 'recall',
    keep: ['.ledger', '.pane'],
    tall: 770,
    phoneTall: 900,
    phoneEnds: '.pane-body > *',
    wide: (page) => flip(page, 'Upgrade Go to 1.25'),
    phone: async (page) => {
      await tab(page, 2)
      await flip(page, 'Upgrade Go to 1.25')
      await tab(page, 3)
    },
  },
  {
    // One fact, opened: its check, the proof, and every time the answer changed.
    name: 'proof',
    keep: ['.ledger', '.pane'],
    tall: 830,
    phoneTall: 820,
    phoneEnds: '.pane-body > *',
    wide: async (page) => {
      await flip(page, 'Stop the API', 'Start the API again')
      await page.click('.legend .pick.t-holds') // only the facts that hold
      await row(page, 'health').locator('.row-main').click()
    },
    phone: async (page) => {
      await tab(page, 2)
      await flip(page, 'Stop the API', 'Start the API again')
      await tab(page, 1)
      await row(page, 'health').locator('.row-main').click()
    },
  },
  {
    // A check the policy does not allow is not run; the policy is beside it.
    name: 'policy',
    keep: ['.ledger', '.pane'],
    tall: 640,
    phoneTall: 820,
    phoneEnds: '.pane-body > *',
    wide: async (page) => {
      await page.click('.foot .link') // "what checks may touch", under the ledger
      await page.evaluate(() => document.querySelector('.ledger').scrollTo(0, 0))
      await page.mouse.move(2, 2) // off the rows, so none is drawn as hovered
    },
    // on a phone there is no ledger beside the pane, so the blocked fact itself is opened
    phone: (page) => row(page, 'billing').locator('.row-main').click(),
  },
]

const SCREEN_WIDTH = 1280

async function box(page, selectors) {
  return page.evaluate((selectors) => {
    const r = selectors.map((s) => document.querySelector(s).getBoundingClientRect())
    const x0 = Math.min(...r.map((b) => b.left))
    const y0 = Math.min(...r.map((b) => b.top))
    return { x: x0, y: y0, width: Math.max(...r.map((b) => b.right)) - x0, height: Math.max(...r.map((b) => b.bottom)) - y0 }
  }, selectors)
}

/** The bottom of the last ledger row that fits whole inside `limit`, or `limit` if the rows stop short of it. */
function lastWholeRow(page, limit) {
  return page.evaluate((limit) => {
    const ends = [...document.querySelectorAll('.ledger .row')].map((r) => Math.round(r.getBoundingClientRect().bottom))
    const fit = ends.filter((y) => y <= limit)
    return ends.some((y) => y > limit) && fit.length ? fit[fit.length - 1] : limit
  }, limit)
}

/** The bottom of the last element matching `sel` that fits whole inside `limit`, or `limit`. */
function lastWhole(page, sel, limit) {
  return page.evaluate(
    ({ sel, limit }) => {
      const ends = [...document.querySelectorAll(sel)]
        .filter((e) => e.getClientRects().length)
        .map((e) => Math.round(e.getBoundingClientRect().bottom))
        .filter((y) => y > 0 && y <= limit)
      return ends.length ? Math.max(...ends) : limit
    },
    { sel, limit },
  )
}

/** PROBE=1 prints where each column's content and its sections end, for choosing a screen's `tall`. */
function probe(page) {
  return page.evaluate(() => {
    const ends = (sel) => [...document.querySelectorAll(sel)].map((e) => Math.round(e.getBoundingClientRect().bottom))
    const whole = (sel) => {
      const e = document.querySelector(sel)
      return e ? Math.round(e.getBoundingClientRect().top + e.scrollHeight) : null
    }
    return {
      window: innerHeight,
      rail: whole('.rail'),
      ledger: whole('.ledger'),
      pane: whole('.pane'),
      rows: ends('.ledger .row'),
      sections: ends('.pane-body > *'),
      switches: ends('.rail .switch'),
    }
  })
}

// The columns are the same width whatever the demo shows, so they are measured
// once; each screen is then opened at the scale that makes its picture exactly
// WIDE pixels across.
let widths
async function measure() {
  const server = await demo()
  try {
    const page = await open(server, { width: SCREEN_WIDTH, height: 800, dpr: 1, theme: 'dark' })
    widths = {}
    for (const s of SCREENS) widths[s.name] = (await box(page, s.keep)).width
    await page.context().close()
  } finally {
    server.stop()
  }
}

async function screen(s, theme, phone) {
  const server = await demo()
  try {
    const view = phone
      ? { width: PHONE.width, height: s.phoneTall, dpr: PHONE.dpr }
      : { width: SCREEN_WIDTH, height: s.tall + (s.keep.includes('.bar') ? 0 : 48), dpr: WIDE / widths[s.name] }
    const page = await open(server, { ...view, theme, phone })
    await (phone ? s.phone : s.wide)(page)
    await settle(page)
    let clip = { x: 0, y: 0, width: view.width, height: view.height }
    if (phone) {
      // a phone picture ends where a row or a section of the pane ends, never through one
      clip.height = await lastWhole(page, s.phoneEnds ?? '.ledger .row', view.height)
      if (process.env.PROBE) console.log(s.name, 'phone', JSON.stringify(await probe(page)))
    } else {
      const height = await lastWholeRow(page, view.height)
      if (height !== view.height) {
        await page.setViewportSize({ width: view.width, height })
        await settle(page, 300)
      }
      clip = await box(page, s.keep)
      clip.height = height - clip.y
      if (process.env.PROBE) console.log(s.name, JSON.stringify(await probe(page)))
    }
    const png = await page.screenshot({ clip, type: 'png' })
    await save(s.name, theme, phone, await finish(png, { line: await line(page) }))
    await page.context().close()
  } finally {
    server.stop()
  }
}

// ---- the terminal pictures ----

// The sessions are run for real, in a throwaway folder, every time the
// pictures are made; what is typeset below is what the binary printed.

/** Run the binary in a folder as "alex" and return everything it printed. */
function hf(cwd, state, ...args) {
  const r = spawnSync(bin, args, {
    cwd,
    encoding: 'utf8',
    env: { PATH: process.env.PATH, HOME: state, HOLDFAST_STATE: state, HOLDFAST_AGENT: 'alex', NO_COLOR: '1' },
  })
  if (r.error) throw r.error
  return (r.stdout + r.stderr).trimEnd()
}

/** An argument as a person would type it in a shell. */
const sh = (a) => (/^[\w@%+=:,./-]+$/.test(a) ? a : /["$`\\]/.test(a) ? `'${a}'` : `"${a}"`)

/** One step of a session: the sentence above it, the command, and what came back. */
const step = (note, cwd, state, ...args) => ({
  note,
  cmd: ['holdfast', ...args].map(sh).join(' '),
  out: hf(cwd, state, ...args),
})

function project(root, name) {
  const dir = path.join(root, name)
  fs.mkdirSync(dir)
  fs.writeFileSync(path.join(dir, 'go.mod'), 'module example.com/atlas\n\ngo 1.24\n')
  fs.writeFileSync(path.join(dir, '.env'), 'PORT=8080\nDATABASE_URL=postgres://localhost:5432/atlas\n')
  fs.writeFileSync(path.join(dir, '.gitignore'), '.env\n')
  return dir
}

function sessions() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'holdfast-shots-'))
  try {
    const here = path.join(root, 'state-here')
    const there = path.join(root, 'state-there')
    fs.mkdirSync(here)
    fs.mkdirSync(there)

    // A claim has to be true at the moment it is stored.
    const a = project(root, 'atlas')
    hf(a, here, 'init')
    const refused = [
      step('A claim that is not true right now is refused.', a, here,
        'add', 'Go is pinned to 1.25', '--check', 'contains go.mod "go 1.25"'),
      step('A true one is stored, and its proof is stored with it.', a, here,
        'add', 'Go is pinned to 1.24', '--check', 'contains go.mod "go 1.24"'),
    ]

    // The facts travel with the repository; each machine proves them for itself.
    const b = project(root, 'laptop')
    hf(b, here, 'init')
    hf(b, here, 'add', 'Go is pinned to 1.24', '--check', 'contains go.mod "go 1.24"')
    hf(b, here, 'add', 'The app reads its database address from .env', '--check', 'contains .env DATABASE_URL=')
    const first = step('On the machine where the facts were stored, both hold.', b, here, 'list')
    const git = (cwd, ...args) =>
      execFileSync('git', ['-c', 'user.name=alex', '-c', 'user.email=alex@example.com', ...args], { cwd, stdio: 'ignore' })
    git(b, 'init', '-q', '-b', 'main', '.')
    git(b, 'add', '-A')
    git(b, 'commit', '-q', '-m', 'remember how the project is set up')
    const c = path.join(root, 'clone')
    git(root, 'clone', '-q', b, c)
    const shared = [
      first,
      step('On a fresh clone the facts are there, and .env is not. The proof is made again here.', c, there, 'check'),
    ]
    return { refused, shared }
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
}

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
const LABELS = 'HOLDS|BROKEN|BLOCKED|UNPROVEN|STALE|TIMED OUT'
const tone = (label) => ({ HOLDS: 'holds', BROKEN: 'broken', BLOCKED: 'blocked', STALE: 'stale', 'TIMED OUT': 'stale' })[label] ?? 'plain'

/** One printed line, set so that a line too long for the picture wraps under its own text. */
function printed(l) {
  if (l === '') return '<div class="gap"></div>'
  let m
  if ((m = l.match(new RegExp(`^(${LABELS})(\\s+)(f-\\w+)(\\s+)(.*)$`)))) {
    const lead = m[1].length + m[2].length + m[3].length + m[4].length
    return (
      `<div class="o fact" style="padding-left:${lead}ch;text-indent:-${lead}ch">` +
      `<b class="t-${tone(m[1])}">${m[1]}</b>${m[2]}<i>${m[3]}</i>${m[4]}${esc(m[5])}</div>`
    )
  }
  if ((m = l.match(new RegExp(`^(${LABELS}|Not stored)(:| and )(.*)$`)))) {
    const t = m[1] === 'Not stored' ? 'broken' : tone(m[1])
    return `<div class="o"><b class="t-${t}">${m[1]}</b>${esc(m[2] + m[3])}</div>`
  }
  if ((m = l.match(/^( +)(.*)$/))) return `<div class="o" style="padding-left:${m[1].length}ch">${esc(m[2])}</div>`
  return `<div class="o head">${esc(l)}</div>`
}

const terminal = (steps) =>
  `<div class="set term">` +
  steps
    .map(
      (s) =>
        `<section class="step"><p class="note">${esc(s.note)}</p>` +
        `<div class="cmd"><span class="p">$ </span>${esc(s.cmd)}</div>` +
        `<div class="out">${s.out.split('\n').map(printed).join('')}</div></section>`,
    )
    .join('') +
  `</div>`

// ---- the numbers ----

const figures = () =>
  `<div class="set nums">` +
  numbers
    .map((n) => `<div class="num"><b>${esc(n.value)}</b><span>${esc(n.label)}</span><small>${esc(n.more)}</small></div>`)
    .join('') +
  `</div>`

// ---- typesetting ----

// These pictures are set on the page that ships in the binary, so they use
// its type and its colours, in whichever theme the picture is for.
const SET = `
html, body { height: auto !important; min-height: 0 !important; overflow: visible !important; }
body { margin: 0; background: var(--bg); }
.set { box-sizing: border-box; width: 100vw; background: var(--bg); color: var(--text-2); }
.set b { font-weight: 500; }
.set .t-holds { color: var(--holds); } .set .t-broken { color: var(--broken); }
.set .t-blocked { color: var(--blocked); } .set .t-stale { color: var(--stale); } .set .t-plain { color: var(--text-2); }

.term { padding: 28px 32px 30px; background: var(--sunk); font: 400 13.5px/1.6 var(--mono); font-feature-settings: 'zero' 0; }
.term .step + .step { margin-top: 22px; padding-top: 22px; border-top: 1px solid var(--line); }
.term .note { margin: 0 0 12px; font: 500 15px/1.4 var(--sans); color: var(--text); text-wrap: balance; }
.term .cmd { padding-left: 2ch; text-indent: -2ch; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--text); }
.term .cmd .p { color: var(--text-3); }
.term .out { margin-top: 8px; padding-left: 2ch; }
.term .o { white-space: pre-wrap; overflow-wrap: anywhere; }
.term .gap { height: 0.75em; }
.term .fact, .term .head { color: var(--text); }
.term .fact i { font-style: normal; color: var(--text-3); }

.nums { display: grid; grid-template-columns: repeat(5, 1fr); }
.num { display: grid; align-content: start; gap: 6px; padding: 26px 22px 28px; }
.num + .num { border-left: 1px solid var(--line); }
.num b { font: 600 44px/1 var(--sans); letter-spacing: -0.03em; color: var(--text); font-variant-numeric: tabular-nums; }
.num span { margin-top: 8px; font: 500 14.5px/1.3 var(--sans); color: var(--text); }
.num small { font: 400 12.5px/1.45 var(--sans); color: var(--text-3); }

@media (max-width: 600px) {
  .term { padding: 20px 16px 22px; font-size: 11.25px; line-height: 1.6; }
  .term .note { font-size: 14px; margin-bottom: 10px; }
  .term .step + .step { margin-top: 18px; padding-top: 18px; }
  .term .out { padding-left: 0; }
  .nums { grid-template-columns: 1fr; padding: 4px 0; }
  .num { grid-template-columns: 98px 1fr; column-gap: 14px; row-gap: 3px; padding: 16px 18px; align-items: baseline; }
  .num + .num { border-left: 0; border-top: 1px solid var(--line); }
  .num b { grid-row: span 2; font-size: 35px; align-self: center; }
  .num span { margin-top: 0; }
}
`

/** Set a picture on the demo's page and capture it. */
async function typeset(server, name, theme, phone, html) {
  const page = await open(server, {
    width: phone ? PHONE.width : WIDE / 2,
    height: 900,
    dpr: phone ? PHONE.dpr : 2,
    theme,
    phone,
  })
  const hairline = await line(page)
  await page.addStyleTag({ content: SET })
  await page.evaluate((html) => (document.body.innerHTML = html), html)
  await page.evaluate(() => document.fonts.ready)
  await settle(page, 300)
  const png = await page.locator('.set').screenshot({ type: 'png' })
  await save(name, theme, phone, await finish(png, { line: hairline }))
  await page.context().close()
}

// ---- the social card ----

// docs/readme/social.png, 1280 x 640: the card a link to the repository
// unfurls into. The ledger in it is the real page, in the hero's state.
const MARK =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">' +
  '<path d="M12 2.5V10"/><path d="M12 10c0 4-5 3.6-6.4 8.2"/><path d="M12 10v6.6"/>' +
  '<path d="M12 10c0 4 5 3.6 6.4 8.2"/><path d="M3 21.5c3.2-4.6 14.8-4.6 18 0"/></svg>'

const CARD = `
.card { position: relative; width: 1280px; height: 640px; overflow: hidden; background: var(--bg); color: var(--text); font-family: var(--sans); }
.card .words { position: absolute; left: 72px; top: 64px; bottom: 60px; width: 500px; display: flex; flex-direction: column; }
.card .brand { display: flex; align-items: center; gap: 12px; font: 600 26px/1 var(--sans); letter-spacing: -0.01em; }
.card .brand svg { width: 34px; height: 34px; }
.card h1 { margin: auto 0 0; font: 600 72px/1 var(--sans); letter-spacing: -0.035em; text-wrap: balance; }
.card p { margin: 26px 0 0; font: 400 23px/1.4 var(--sans); color: var(--text-2); text-wrap: pretty; }
.card .where { margin-top: auto; font: 400 17px/1 var(--mono); color: var(--text-3); }
.card .real { position: absolute; left: 640px; top: 64px; width: 640px; border: 1px solid var(--line); border-radius: 10px 0 0 0; }
`

async function social() {
  const server = await demo()
  try {
    const page = await open(server, { width: SCREEN_WIDTH, height: 900, dpr: 2, theme: 'dark' })
    await flip(page, 'Stop the API', 'Upgrade Go to 1.25')
    await page.mouse.move(2, 2)
    await settle(page)
    const ledger = await page.locator('.ledger').screenshot({ type: 'png' })
    await page.setViewportSize({ width: 1280, height: 640 })
    await page.addStyleTag({ content: SET + CARD })
    await page.evaluate(
      (html) => (document.body.innerHTML = html),
      `<div class="set card"><div class="words">` +
        `<div class="brand">${MARK}Holdfast</div>` +
        `<h1>Every fact carries its proof.</h1>` +
        `<p>A memory for coding agents. What it remembers is checked again before the agent may rely on it.</p>` +
        `<div class="where">github.com/gallowforge/holdfast</div>` +
        `</div><img class="real" alt="" src="data:image/png;base64,${ledger.toString('base64')}"></div>`,
    )
    await page.evaluate(() => Promise.all([document.fonts.ready, ...[...document.images].map((i) => i.decode())]))
    await settle(page, 300)
    const png = await page.locator('.card').screenshot({ type: 'png', scale: 'css' }) // 1280 x 640 exactly
    fs.writeFileSync(path.join(out, 'social.png'), png)
    console.log(`${'social.png'.padEnd(30)} ${(png.length / 1024).toFixed(0).padStart(5)} KB`)
    await page.context().close()
  } finally {
    server.stop()
  }
}

// ---- run ----

const wanted = (name) => !only || only.includes(name)
const themes = process.env.THEME ? [process.env.THEME] : THEMES
const sizes = process.env.SIZE ? [process.env.SIZE] : ['wide', 'phone']

browser = await chromium.launch(process.env.SHOTS_CHANNEL ? { channel: process.env.SHOTS_CHANNEL } : {})
try {
  bench = await (await browser.newContext()).newPage()
  await measure()
  for (const s of SCREENS) {
    if (!wanted(s.name)) continue
    for (const theme of themes) for (const size of sizes) await screen(s, theme, size === 'phone')
  }
  const sets = ['refused', 'shared', 'numbers'].filter(wanted)
  if (sets.length) {
    const said = sets.some((n) => n !== 'numbers') ? sessions() : {}
    const server = await demo()
    try {
      for (const name of sets) {
        const html = name === 'numbers' ? figures() : terminal(said[name])
        for (const theme of themes) for (const size of sizes) await typeset(server, name, theme, size === 'phone', html)
      }
    } finally {
      server.stop()
    }
  }
  if (wanted('social')) await social()
} finally {
  await browser.close()
}
