// Capture Holdfast for the Glass scenes, in its dark theme and its light theme.
//   node capture.cjs [path to the binary]        everything (the binary defaults to ../../../bin/holdfast)
//   ONLY=screens,terms,replay node capture.cjs   only those parts
// Three kinds of capture, all of them the real thing:
//   screens  the page of `holdfast serve --demo`, 1120 px wide at 2x and 390 px wide at 3x, after the demo's own
//            switches changed its pretend machine. A fresh demo for each, so every one starts from the same machine.
//   terms    command-line sessions, run for real in a throwaway folder and typeset on the demo's page, so they
//            use its type and its colours.
//   replay   the hero's moving part: the ledger while "Put everything back", "Stop the API" and "Upgrade Go to
//            1.25" are pressed, photographed twelve times a second with the page's own animations stepped by hand.
// Named elements are measured into build/shots/boxes.json ([x, y, w, h, background] in capture pixels) so a scene
// can lift one out of its screen by name. The scenes only crop, place and light what is captured here.
const { chromium } = require('playwright');
const { spawn, spawnSync, execFileSync } = require('child_process');
const net = require('net'), os = require('os'), path = require('path'), fs = require('fs'), { pathToFileURL } = require('url');

const BIN = path.resolve(process.argv[2] || path.join(__dirname, '..', '..', '..', 'bin', 'holdfast'));
const SHOTS = path.resolve(__dirname, '..', 'build', 'shots');
const ONLY = process.env.ONLY ? process.env.ONLY.split(',') : ['screens', 'terms', 'replay'];
const DESK = { width: 1120, height: 940, dpr: 2 };
const PHONE = { width: 390, height: 1500, dpr: 3 };      // taller than a phone, so a scene can read further down the page
const TERM = 560;                                        // a terminal is set narrower than the page, so its type stays large on the glass
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- the demo ----------
const freePort = () => new Promise((res, rej) => {
  const s = net.createServer();
  s.once('error', rej);
  s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => res(port)); });
});
async function demo() {
  const port = await freePort();
  const proc = spawn(BIN, ['serve', '--demo', '--addr', `127.0.0.1:${port}`], { stdio: 'ignore' });
  const url = `http://127.0.0.1:${port}/`;
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(url)).ok) return { url, stop: () => proc.kill('SIGINT') }; } catch { /* not up yet */ }
    await sleep(100);
  }
  proc.kill('SIGINT');
  throw new Error('the demo did not start: ' + BIN);
}
const context = (browser, scheme, phone, extra = {}) => browser.newContext(phone
  ? { viewport: { width: PHONE.width, height: PHONE.height }, deviceScaleFactor: PHONE.dpr, colorScheme: scheme, isMobile: true, hasTouch: true, reducedMotion: 'reduce', ...extra }
  : { viewport: { width: DESK.width, height: DESK.height }, deviceScaleFactor: DESK.dpr, colorScheme: scheme, reducedMotion: 'reduce', ...extra });

// ---------- the screens ----------
const tab = (p, n) => p.click(`.tabs button:nth-child(${n})`);      // a phone shows one column at a time: 1 ledger, 2 machine, 3 recall
const openRow = async (p, text) => { await p.locator('.row', { hasText: text }).first().locator('.row-main').click(); await p.waitForTimeout(400); };
async function change(p, phone, ...labels) {
  if (phone) await tab(p, 2);
  for (const label of labels) {
    await p.locator('.switch button', { hasText: label }).first().click();
    await p.waitForFunction(() => !document.querySelector('.switch.is-busy'));
    await p.waitForTimeout(700);
  }
  if (phone) await tab(p, 1);
}
// [name, what to do on the page first]
const SCREENS = [
  ['calm', null],
  ['broken', (p, phone) => change(p, phone, 'Stop the API', 'Upgrade Go to 1.25')],
  ['recall', async (p, phone) => { await change(p, phone, 'Upgrade Go to 1.25'); if (phone) await tab(p, 3); }],
  ['proof', async (p, phone) => {
    await change(p, phone, 'Stop the API', 'Start the API again');
    await p.click('.legend .pick.t-holds');
    await openRow(p, 'health endpoint');
  }],
  ['holds', (p) => openRow(p, 'Docker is running')],
  ['policy', async (p) => { await p.click('.foot .link'); await p.waitForTimeout(400); }],
  ['blocked', (p) => openRow(p, 'billing service')],
];
// A mark is { css, text?, index?, and? }: the element matching `css` whose text holds `text` (index -1 = the last
// match), joined with the first match of `and` when that is given. A mark that is not on a screen is left out.
const MARKS = {
  say: { css: '.headline', and: '.legend' }, summary: { css: '.summary' }, attention: { css: '.group.g-attention' },
  go: { css: '.ledger .row', text: 'Go is pinned' }, api: { css: '.ledger .row', text: 'listens on port' },
  health: { css: '.ledger .row', text: 'health endpoint' }, billing: { css: '.ledger .row', text: 'billing service' },
  on: { css: '.switch.is-on' }, asks: { css: '.asks' },
  file: { css: '.said-file' }, holdfast: { css: '.said-holdfast' }, said: { css: '.said-file', and: '.said-holdfast' },
  status: { css: '.pane-head' }, claim: { css: '.fact-claim', and: '.fact-id' }, proof: { css: '.part.proof' },
  known: { css: '.fact-claim', and: '.part.proof' }, check: { css: '.part:has(.checkline)' }, history: { css: '.part:has(.history)' },
  rules: { css: '.rules' }, standing: { css: '.standing' },
};
const TERM_MARKS = {
  step0: { css: '.step', index: 0 }, step1: { css: '.step', index: 1 },
  run0: { css: '.step:nth-child(1) .cmd', and: '.step:nth-child(1) .out' }, run1: { css: '.step:nth-child(2) .cmd', and: '.step:nth-child(2) .out' },
  out0: { css: '.step:nth-child(1) .out' }, out1: { css: '.step:nth-child(2) .out' },
};

// The page's colours are oklch, so a 1 px canvas turns a background into the sRGB the capture holds.
async function marksOf(page, marks, k, clip = { x: 0, y: 0 }) {
  return page.evaluate(({ marks, k, clip }) => {
    const cv = document.createElement('canvas'); cv.width = cv.height = 1;
    const cx = cv.getContext('2d', { willReadFrequently: true });
    const rgba = (css) => { cx.clearRect(0, 0, 1, 1); cx.fillStyle = css; cx.fillRect(0, 0, 1, 1); return cx.getImageData(0, 0, 1, 1).data; };
    const out = {};
    for (const [key, m] of Object.entries(marks)) {
      const els = [...document.querySelectorAll(m.css)].filter((el) => el.offsetParent !== null && (!m.text || el.textContent.includes(m.text)));
      const el = els[m.index === -1 ? els.length - 1 : (m.index || 0)];
      if (!el) continue;
      let r = el.getBoundingClientRect();
      if (m.and) {
        const other = document.querySelector(m.and);
        if (!other) continue;
        const o = other.getBoundingClientRect(), left = Math.min(r.left, o.left), top = Math.min(r.top, o.top);
        r = { left, top, width: Math.max(r.right, o.right) - left, height: Math.max(r.bottom, o.bottom) - top };
      }
      let e = el, d = [0, 0, 0, 0];
      while (e && d[3] < 250) { d = rgba(getComputedStyle(e).backgroundColor); e = e.parentElement; }
      const hex = '#' + [d[0], d[1], d[2]].map((v) => v.toString(16).padStart(2, '0')).join('');
      out[key] = [(r.left + window.scrollX - clip.x) * k, (r.top + window.scrollY - clip.y) * k, r.width * k, r.height * k].map(Math.round).concat(hex);
    }
    return out;
  }, { marks, k, clip });
}

async function screens(browser, boxes) {
  for (const scheme of ['dark', 'light']) for (const phone of [false, true]) for (const [name, prepare] of SCREENS) {
    const d = await demo();
    try {
      const ctx = await context(browser, scheme, phone);
      const page = await ctx.newPage();
      await page.goto(d.url);
      await page.waitForSelector('.row');
      await page.evaluate(() => document.fonts.ready);
      if (prepare) await prepare(page, phone);
      // read from the top, with the pointer off the page's controls
      await page.evaluate(() => { for (const el of document.querySelectorAll('.rail, .ledger, .pane')) el.scrollTop = 0; window.scrollTo(0, 0); });
      if (!phone) await page.mouse.move(2, 2);
      await page.waitForTimeout(500);
      const file = `${phone ? 'm-' : ''}${name}-${scheme}`;
      boxes[file] = await marksOf(page, MARKS, phone ? PHONE.dpr : DESK.dpr);
      await page.screenshot({ path: path.join(SHOTS, file + '.png') });
      console.log(file);
      await ctx.close();
    } finally { d.stop(); }
  }
}

// ---------- the command line ----------
// The sessions are run for real, in a throwaway folder, every time the pictures are made; what is typeset is what
// the binary printed.
function hf(cwd, state, ...args) {
  const r = spawnSync(BIN, args, { cwd, encoding: 'utf8', env: { PATH: process.env.PATH, HOME: state, HOLDFAST_STATE: state, HOLDFAST_AGENT: 'alex', NO_COLOR: '1' } });
  if (r.error) throw r.error;
  return (r.stdout + r.stderr).trimEnd();
}
const sh = (a) => (/^[\w@%+=:,./-]+$/.test(a) ? a : /["$`\\]/.test(a) ? `'${a}'` : `"${a}"`);      // an argument as a person would type it
const step = (note, cwd, state, ...args) => ({ note, cmd: ['holdfast', ...args].map(sh).join(' '), out: hf(cwd, state, ...args) });
function project(root, name) {
  const dir = path.join(root, name);
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, 'go.mod'), 'module example.com/atlas\n\ngo 1.24\n');
  fs.writeFileSync(path.join(dir, '.env'), 'PORT=8080\nDATABASE_URL=postgres://localhost:5432/atlas\n');
  fs.writeFileSync(path.join(dir, '.gitignore'), '.env\n');
  return dir;
}
function sessions() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'holdfast-shots-'));
  try {
    const here = path.join(root, 'state-here'), there = path.join(root, 'state-there');
    fs.mkdirSync(here); fs.mkdirSync(there);
    // A claim has to be true at the moment it is stored.
    const a = project(root, 'atlas');
    hf(a, here, 'init');
    const refused = [
      step('A claim that is not true right now is refused.', a, here, 'add', 'Go is pinned to 1.25', '--check', 'contains go.mod "go 1.25"'),
      step('A true one is stored, and its proof is stored with it.', a, here, 'add', 'Go is pinned to 1.24', '--check', 'contains go.mod "go 1.24"'),
    ];
    // The facts travel with the repository; each machine proves them for itself.
    const b = project(root, 'laptop');
    hf(b, here, 'init');
    hf(b, here, 'add', 'Go is pinned to 1.24', '--check', 'contains go.mod "go 1.24"');
    hf(b, here, 'add', 'The app reads its database address from .env', '--check', 'contains .env DATABASE_URL=');
    const first = step('On the machine where the facts were stored, both hold.', b, here, 'list');
    const git = (cwd, ...args) => execFileSync('git', ['-c', 'user.name=alex', '-c', 'user.email=alex@example.com', ...args], { cwd, stdio: 'ignore' });
    git(b, 'init', '-q', '-b', 'main', '.');
    git(b, 'add', '-A');
    git(b, 'commit', '-q', '-m', 'remember how the project is set up');
    const c = path.join(root, 'clone');
    git(root, 'clone', '-q', b, c);
    const shared = [first, step('On a fresh clone the facts are there, and .env is not. The proof is made again here.', c, there, 'check')];
    return { refused, shared };
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
}
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const LABELS = 'HOLDS|BROKEN|BLOCKED|UNPROVEN|STALE|TIMED OUT';
const tone = (label) => ({ HOLDS: 'holds', BROKEN: 'broken', BLOCKED: 'blocked', STALE: 'stale', 'TIMED OUT': 'stale' })[label] || 'plain';
// One printed line, set so that a line too long for the picture wraps under its own text.
function printed(l) {
  if (l === '') return '<div class="gap"></div>';
  let m;
  if ((m = l.match(new RegExp(`^(${LABELS})(\\s+)(f-\\w+)(\\s+)(.*)$`)))) {
    const lead = m[1].length + m[2].length + m[3].length + m[4].length;
    return `<div class="o fact" style="padding-left:${lead}ch;text-indent:-${lead}ch"><b class="t-${tone(m[1])}">${m[1]}</b>${m[2]}<i>${m[3]}</i>${m[4]}${esc(m[5])}</div>`;
  }
  if ((m = l.match(new RegExp(`^(${LABELS}|Not stored)(:| and )(.*)$`)))) {
    return `<div class="o"><b class="t-${m[1] === 'Not stored' ? 'broken' : tone(m[1])}">${m[1]}</b>${esc(m[2] + m[3])}</div>`;
  }
  if ((m = l.match(/^( +)(.*)$/))) return `<div class="o" style="padding-left:${m[1].length}ch">${esc(m[2])}</div>`;
  return `<div class="o head">${esc(l)}</div>`;
}
const terminal = (steps) => `<div class="term">${steps.map((s) =>
  `<section class="step"><p class="note">${esc(s.note)}</p><div class="cmd"><span class="p">$ </span>${esc(s.cmd)}</div>` +
  `<div class="out">${s.out.split('\n').map(printed).join('')}</div></section>`).join('')}</div>`;
const SET = `
html, body { height: auto !important; min-height: 0 !important; overflow: visible !important; }
body { margin: 0; background: var(--bg); }
.term { box-sizing: border-box; width: 100vw; padding: 26px 30px 30px; background: var(--bg); color: var(--text-2); font: 400 13.5px/1.6 var(--mono); font-feature-settings: 'zero' 0; }
.term b { font-weight: 500; }
.term .t-holds { color: var(--holds); } .term .t-broken { color: var(--broken); }
.term .t-blocked { color: var(--blocked); } .term .t-stale { color: var(--stale); } .term .t-plain { color: var(--text-2); }
.term .step + .step { margin-top: 22px; padding-top: 22px; border-top: 1px solid var(--line); }
.term .note { margin: 0 0 12px; font: 500 15px/1.4 var(--sans); color: var(--text); text-wrap: balance; }
.term .cmd { padding-left: 2ch; text-indent: -2ch; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--text); }
.term .cmd .p { color: var(--text-3); }
.term .out { margin-top: 8px; padding-left: 2ch; }
.term .o { white-space: pre-wrap; overflow-wrap: anywhere; }
.term .gap { height: 0.75em; }
.term .fact, .term .head { color: var(--text); }
.term .fact i { font-style: normal; color: var(--text-3); }
@media (max-width: 500px) {
  .term { padding: 20px 16px 22px; font-size: 11.25px; }
  .term .note { font-size: 14px; margin-bottom: 10px; }
  .term .step + .step { margin-top: 18px; padding-top: 18px; }
  .term .out { padding-left: 0; }
}`;
async function terms(browser, boxes) {
  const told = sessions();
  const d = await demo();
  try {
    for (const scheme of ['dark', 'light']) for (const phone of [false, true]) {
      const ctx = await context(browser, scheme, phone, phone ? {} : { viewport: { width: TERM, height: 700 } });
      const page = await ctx.newPage();
      await page.goto(d.url);
      await page.waitForSelector('.row');
      await page.addStyleTag({ content: SET });
      for (const name of ['refused', 'shared']) {
        await page.evaluate((html) => { document.body.innerHTML = html; }, terminal(told[name]));
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(300);
        const file = `${phone ? 'm-' : ''}${name}-${scheme}`;
        boxes[file] = await marksOf(page, TERM_MARKS, phone ? PHONE.dpr : DESK.dpr);
        await page.locator('.term').screenshot({ path: path.join(SHOTS, file + '.png') });
        console.log(file);
      }
      await ctx.close();
    }
  } finally { d.stop(); }
}

// ---------- the hero's moving part ----------
// The ledger and the machine beside it, from "3 facts are no longer true." back to a machine where every check
// holds, and broken again: three presses of the demo's own switches. The page's clock is held and its animations
// (rows gliding to where they belong, the strike through a broken claim, the bar of states) are stepped by hand,
// REPLAY.step ms a frame, so every build photographs the same moments. Frame 0 is the settled picture the loop
// opens on; each press gives REPLAY.turn frames of motion and then one settled frame.
async function replay(browser, scheme, boxes, { turn: N, step: STEP, clip }) {
  const d = await demo();
  try {
    const ctx = await context(browser, scheme, false, { reducedMotion: 'no-preference' });
    const page = await ctx.newPage();
    await page.clock.install();
    await page.goto(d.url);
    await page.waitForSelector('.row');
    await page.evaluate(() => document.fonts.ready);
    const press = (label) => page.evaluate((label) => {      // a press with no pointer, so nothing is left hovered
      [...document.querySelectorAll('.rail button')].find((b) => b.textContent.includes(label)).click();
    }, label);
    const state = () => page.evaluate(() => document.querySelector('.headline').textContent + '|' +
      [...document.querySelectorAll('.ledger .row')].map((r) => r.dataset.flip + r.className).join() + '|' + !!document.querySelector('.switch.is-busy'));
    const pressed = async (label) => {
      const was = await state();
      await press(label);
      for (let i = 0; i < 400; i++) { const now = await state(); if (now !== was && now.endsWith('false')) return; await sleep(20); }
      throw new Error(`"${label}" changed nothing`);
    };
    await page.mouse.move(2, 2);
    await page.clock.pauseAt(await page.evaluate(() => Date.now() + 100));
    const finish = () => page.evaluate(() => { for (const a of document.getAnimations()) { try { a.finish(); } catch { /* one that never ends */ } } });
    // let a press come to rest: end what is moving, let the page clear its marks, and stay inside "just now"
    const rest = async () => { await finish(); await page.clock.runFor(1700); await finish(); await page.waitForTimeout(150); };
    for (const label of ['Stop the API', 'Upgrade Go to 1.25']) { await pressed(label); await rest(); }
    let k = 0;
    const shot = () => page.screenshot({ path: path.join(SHOTS, `replay-${String(k++).padStart(2, '0')}-${scheme}.png`), clip });
    boxes[`replay-00-${scheme}`] = await marksOf(page, MARKS, DESK.dpr, clip);
    await shot();
    const says = [await page.locator('.headline').textContent()];
    for (const label of ['Put everything back', 'Stop the API', 'Upgrade Go to 1.25']) {
      await pressed(label);
      for (let i = 0; i < N; i++) {
        await page.evaluate((t) => {
          for (const a of document.getAnimations()) { if (a.t0 === undefined) a.t0 = t; a.pause(); a.currentTime = t - a.t0; }
        }, i * STEP);
        await shot();
        await page.clock.runFor(STEP);
      }
      await rest();
      says.push(await page.locator('.headline').textContent());
      if (label !== 'Upgrade Go to 1.25') await shot();
    }
    console.log(`replay-${scheme}: ${k} frames; ${says.join(' / ')}`);
    await ctx.close();
  } finally { d.stop(); }
}

(async () => {
  if (!fs.existsSync(BIN)) throw new Error(`no binary at ${BIN}: run \`make build\` first`);
  fs.mkdirSync(SHOTS, { recursive: true });
  const boxFile = path.join(SHOTS, 'boxes.json');
  const boxes = fs.existsSync(boxFile) ? JSON.parse(fs.readFileSync(boxFile, 'utf8')) : {};
  const { REPLAY } = await import(pathToFileURL(path.join(__dirname, 'layouts.js')).href);
  const browser = await chromium.launch();
  try {
    if (ONLY.includes('screens')) await screens(browser, boxes);
    if (ONLY.includes('terms')) await terms(browser, boxes);
    if (ONLY.includes('replay')) for (const scheme of ['dark', 'light']) await replay(browser, scheme, boxes, REPLAY);
  } finally { await browser.close(); }
  fs.writeFileSync(boxFile, JSON.stringify(boxes, null, 1));
})().catch((e) => { console.error(e); process.exit(1); });
