// Holdfast's README scenes: which captures float where, what is lifted out of them, and the words beside them.
// The pictures inside the glass are made by capture.cjs from the running demo and from real command-line
// sessions; this file only crops, places and lights them. Sizes are CSS px of the picture.
import NUMBERS from './numbers.json' with { type: 'json' };

// The hero's moving part, as capture.cjs photographs it: `turn` frames of motion for each press of a switch,
// `step` ms apart, cut to the machine and the ledger beside it.
export const REPLAY = { turn: 12, step: 1000 / 12, clip: { x: 0, y: 0, width: 764, height: 620 } };

// ---------- the room ----------
// Holdfast's own colours: a blue-black ground, coral for what broke, green for what holds, violet for what a
// policy stopped. The room is the ground with a little of one of them low in the haze.
const CORAL = '#fb784f', GREEN = '#56d1a3', VIOLET = '#a7a5eb';
const NIGHT = { page: '#0d1117',
  env: { top: '#0a0f14', bot: '#05080b', haze: '#172631', hazeStr: 0.10, ray: '#bcd3dd',
    shafts: [[0.50, 0.05, 0.04, 0.09], [0.70, 0.025, 0.02, 0.06], [0.88, 0.07, -0.04, 0.09], [0.72, 0.22, 0.0, 0.035]],
    glows: [[0.72, 1.1, 0.6, 0.07, '#b4c8c6'], [0.70, 0.30, 0.42, 0.05, CORAL]] },
  glass: { tint: [0.056, 0.066, 0.074], transmit: 0.80, fog: '#0a0f14', light: [-700, 800, 900], rim: 0.9, spec: 0.40, glow: 0.45, shadow: 0.30, levels: 5, caustic: 0 },
  motes: { n: 60, color: '#dcebf2', amt: 0.45 }, bloom: 0.36, bloomThresh: 0.6 };
const DAY = { page: '#ffffff',
  env: { top: '#eef2f4', bot: '#f8fafb', haze: '#ffffff', hazeStr: 0.04, shadeCol: '#e9eef1', sun: [0.42, 0, -1.15, 1.0], dapple: 0.6, sunStr: 0.075, sunCol: '#fff5e0',
    shadow: 0.55, caustic: 0.10, shadowDir: [0.05, -0.075], glows: [] },
  glass: { tint: [0.072, 0.076, 0.080], transmit: 0.88, fog: '#eef1f3', light: [-700, 800, 900], rim: 0.95, spec: 0.30, glow: 0.0, shadow: 0.12, levels: 5, caustic: 0 },
  motes: { n: 36, color: '#fbfdff', amt: 0.45 }, bloom: 0.0, bloomThresh: 0.95 };
const merge = (a, b) => { const o = { ...a }; for (const k in b) o[k] = (b[k] && typeof b[k] === 'object' && !Array.isArray(b[k])) ? merge(a[k] || {}, b[k]) : b[k]; return o; };
// the same room with another colour low in the haze
const lit = (color, str = 0.05) => ({ env: { glows: [[0.72, 1.1, 0.6, 0.07, '#b4c8c6'], [0.70, 0.30, 0.42, str, color]] } });

// ---------- the type ----------
const C = {
  kick: { night: { color: 'rgba(200,214,224,0.58)' }, day: { color: '#6f7a80' } },
  name: { night: { color: '#f3f6f8' }, day: { color: '#0e1418' } },
  head: { night: { color: '#f3f6f8' }, day: { color: '#0e1418' } },
  line: { night: { color: 'rgba(222,230,236,0.74)' }, day: { color: '#47525a' } },
  fact: { night: { color: '#edf1f4' }, day: { color: '#121a1f' } },
  big: { night: { color: '#f0f4f6' }, day: { color: '#121a1f' } },
  rule: { night: { background: 'rgba(200,216,230,0.16)' }, day: { background: 'rgba(16,28,36,0.12)' } },
};
const T = (cls, html, x, y, style = {}, align) => ({ cls, html, x, y, align, style, ...C[cls] });
const FACT = (v, l) => `<b>${v}</b><span>${l}</span>`;

// ---------- the windows ----------
const BG = (n) => (n ? '#0a1014' : '#f4f7f9');       // the page's ground, keyed out so the page floats on the glass
const tx = (screen, n) => `${screen}-${n ? 'dark' : 'light'}.png`;
// what is lifted out of a window: its own background keyed to glass, a soft shadow on the window behind
const LIFT = (n) => ({ key: 'auto', keyAmt: n ? 0.35 : 0.85, shadowOff: n ? [0, -16, 40] : [10, -22, 26], shadow: n ? 0.45 : 0.22, align: 0.9 });
const WIN = (n, screen, crop, pos, extra = {}) => ({ tex: tx(screen, n), crop, ...pos, key: BG(n), keyAmt: n ? 0.5 : 0.7, bezel: 10, ...extra });
// crops, in capture pixels (the page is captured 1120 px wide at 2x, and 390 px wide at 3x)
const CROP = {
  page: [0, 0, 2240, 1500],            // the whole page: the machine, the ledger, the pane
  ledger: [504, 96, 1736, 1500],       // the ledger and the pane beside it
  centre: [0, 0, 1528, 1100],          // the hero's window: the machine and the ledger
  phone: [0, 0, 1170, 2340],
};

// ---------- a feature panel: the words on the left, one window on the right, one element lifted out of it ----------
function panel(o) {
  const DY = o.dy ?? -20, ty = (o.ty ?? 92) + DY, hl = o.headLines ?? 2;
  const headY = ty + 24, lineY = headY + hl * 38 + 18, ry = (o.ry ?? 318) + DY;
  return {
    css: [840, o.h || 420], fade: [36, 36, 30, 36], cam: [12, 5], pool: o.pool || [0.69, 0.52, 0.37, 0.66],
    night: merge(NIGHT, o.night || {}), day: merge(DAY, o.day || {}),
    windows: (n) => o.win(n, false).map((w) => (w.parent !== undefined ? w : { ...w, y: w.y + DY })),
    type: [
      T('kick', o.kick, 2, ty, { fontSize: '10.5px' }),
      T('head', o.head, 0, headY, { fontSize: '36px' }),
      T('line', o.line, 2, lineY, { fontSize: '15.5px', whiteSpace: 'normal', width: '290px' }),
      T('rule', '', 2, ry, { width: '300px' }),
      ...o.facts.map((fc, i) => T('fact', FACT(fc[0], fc[1]), 2 + i * 102, ry + 18, { width: '92px', whiteSpace: 'normal' })),
    ],
  };
}
// The same panel for a phone: the words on top, the glass under them, the three numbers at the foot.
function panelPhone(o) {
  const ry = o.ryp ?? 600;
  return {
    css: [381, o.hp || 730], dpr: 1080 / 381, fade: [18, 18, 24, 28], cam: [8, 4], pool: o.poolPhone || [0.5, 0.40, 0.70, 0.34],
    night: merge(NIGHT, o.night || {}), day: merge(DAY, o.day || {}),
    windows: (n) => o.win(n, true),
    type: [
      T('kick', o.kick, 4, 26, { fontSize: '9.5px' }),
      T('head', o.head, 2, 46, { fontSize: '31px', whiteSpace: 'normal', width: '372px' }),
      T('line', o.line, 4, 46 + (o.headLinesPhone ?? 2) * 33 + 14, { fontSize: '15px', whiteSpace: 'normal', width: '356px' }),
      T('rule', '', 4, ry, { width: '372px' }),
      ...o.facts.map((fc, i) => T('fact', FACT(fc[0], fc[1]), 4 + i * 124, ry + 18, { width: '112px', whiteSpace: 'normal' })),
    ],
  };
}
// One window and the element lifted out of it. `g` says which capture, which part of it, where it stands and
// what is lifted; `g.phone` says the same for the phone picture, which uses the page as a phone lays it out.
function glass(n, ph, g) {
  const s = ph ? g.phone : g;
  return [
    WIN(n, s.screen, s.crop, { z: ph ? -40 : -60, rot: ph ? [2, -8, 0] : [2, -16, 0], ...s.at }, { breathe: [8, 0.0], ...(s.win || {}) }),
    { parent: 0, box: s.box, pad: [16, 12], lift: 64, breathe: [10, 0.25], bezel: 8, radius: 14, ...LIFT(n), glow: 0.2, ...(s.card || {}) },
  ];
}

const PANELS = {
  recall: {
    kick: 'Recall', head: 'Proved again<br>before it answers',
    line: 'A notes file repeats what was true once. Holdfast runs the check first and says BROKEN when it fails.',
    facts: [['8', 'checks run at&nbsp;once'], ['12 s', 'the most one recall&nbsp;waits'], ['30 s', 'a network proof stays&nbsp;fresh']],
    night: lit(CORAL),
    win: (n, ph) => glass(n, ph, {
      screen: 'recall', crop: [504, 96, 1736, 1500], at: { x: 556, y: 226, w: 372 }, box: 'said', card: { grow: 1.3, pad: [18, 16], align: 0, shift: [4, 92] },
      phone: { screen: 'm-recall', crop: [0, 1410, 1170, 1250], at: { x: 190, y: 386, w: 300 }, box: 'holdfast', card: { grow: 0.97, pad: [18, 16], lift: 54, align: 0, shift: [0, -22] } },
    }),
  },
  refused: {
    kick: 'Storing', head: 'A wrong fact<br>is turned away',
    line: 'A claim must pass its own check at the moment it is stored. An agent cannot write a guess into memory as proof.',
    facts: [['10', 'kinds of check'], ['0', 'failing claims stored'], ['30', 'days until a note goes&nbsp;stale']],
    night: lit(CORAL),
    win: (n, ph) => glass(n, ph, {
      screen: 'refused', crop: null, at: { x: 584, y: 228, w: 350 }, box: 'step0', card: { grow: 1.06, pad: [18, 14], align: 0, shift: [-14, -10] },
      phone: { screen: 'm-refused', crop: null, at: { x: 190, y: 392, w: 290 }, box: 'run0', card: { grow: 1.05, pad: [18, 14], lift: 54, align: 0, shift: [0, -8] } },
    }),
  },
  proof: {
    kick: 'Proof', head: 'Ask any fact<br>how it knows',
    line: 'Open a fact and it shows the evidence from its last check, the check itself, and every time it held or broke.',
    facts: [['133 ms', 'this check took'], ['3', 'results in its history'], ['40', 'results kept for each&nbsp;fact']],
    night: lit(GREEN, 0.04),
    win: (n, ph) => glass(n, ph, {
      screen: 'proof', crop: [504, 96, 1736, 1400], at: { x: 545, y: 228, w: 380 }, box: 'known', card: { grow: 1.3, pad: [18, 16], align: 0, shift: [10, -22] },
      phone: { screen: 'm-proof', crop: [0, 0, 1170, 1340], at: { x: 190, y: 392, w: 290 }, box: 'known', card: { grow: 1.05, pad: [18, 16], lift: 54, align: 0, shift: [0, -6] } },
    }),
  },
  policy: {
    kick: 'Policy', head: 'Outside the policy,<br>nothing runs',
    line: 'A check that reaches past what you allowed does not run. The fact shows BLOCKED, and the one line that would allow it.',
    facts: [['0', 'agent tools can widen&nbsp;it'], ['1', 'line for a person to allow&nbsp;it'], ['10 s', 'limit for a command&nbsp;check']],
    night: lit(VIOLET, 0.06),
    win: (n, ph) => glass(n, ph, {
      screen: 'blocked', crop: [504, 96, 1736, 1400], at: { x: 545, y: 228, w: 380 }, box: 'proof', card: { grow: 1.4, pad: [18, 16], align: 0, shift: [0, 0] },
      phone: { screen: 'm-blocked', crop: [0, 0, 1170, 1400], at: { x: 190, y: 390, w: 290 }, box: 'proof', card: { grow: 1.05, pad: [18, 16], lift: 54, align: 0, shift: [0, 0] } },
    }),
  },
  shared: {
    kick: 'Teams', head: 'Commit the facts,<br>prove them anywhere',
    line: 'The facts travel with the repository. Each machine proves them for itself, so a fresh clone finds what is missing there.',
    facts: [['2', 'facts arrive with the&nbsp;clone'], ['1', 'no longer holds there'], ['0', 'lines of proof in&nbsp;git']],
    night: lit(GREEN, 0.04),
    win: (n, ph) => glass(n, ph, {
      screen: 'shared', crop: null, at: { x: 580, y: 220, w: 258 }, box: 'run1', card: { grow: 1.22, pad: [18, 14], align: 0, shift: [14, 30] },
      phone: { screen: 'm-shared', crop: null, at: { x: 190, y: 392, w: 200 }, box: 'run1', card: { grow: 1.52, pad: [18, 14], lift: 54, align: 0, shift: [0, 20] } },
    }),
  },
};

// ---------- the hero ----------
const HERO_NIGHT = merge(NIGHT, { env: {
  shafts: [[0.30, 0.050, 0.05, 0.13], [0.42, 0.020, 0.02, 0.10], [0.58, 0.022, -0.02, 0.11], [0.71, 0.050, -0.05, 0.13], [0.50, 0.22, 0.0, 0.06]],
  glows: [[0.46, 0.32, 0.42, 0.075, CORAL], [0.5, 1.15, 0.65, 0.09, '#b4c8c6'], [0.5, -0.15, 0.6, 0.05, '#2f5a55'], [0.84, 0.30, 0.26, 0.04, GREEN]] } });
const HERO_DAY = merge(DAY, { env: { sun: [0.10, 0, -0.80, 0.95] } });

// The loop, in frames at 12 a second. It opens on the finished picture, three facts broken and the headline
// lifted out, and holds it. The headline settles back; "Put everything back" is pressed and every check holds;
// then the API is stopped, then Go is upgraded, each as the page really plays it, and the headline lifts again.
const ease = (u) => u * u * (3 - 2 * u);
const R = REPLAY.turn;
const HERO_LOOP = { hold: 22, settle: 6, back: R, calm: 12, stop: R, two: 6, go: R, rest: 5, lift: 6, tail: 3 };
export const HERO_FRAMES = Object.values(HERO_LOOP).reduce((a, b) => a + b, 0);
// The captures in order: 0 three broken, 1..R back, R+1 every check holds, R+2..2R+1 the API stops, 2R+2 two
// broken, 2R+3..3R+2 Go is upgraded; after that comes 0 again.
const replayFrames = (n) => Array.from({ length: 3 * R + 3 }, (_, i) => tx(`replay-${String(i).padStart(2, '0')}`, n));
function heroPlay(phase) {
  const L = HERO_LOOP;
  let f = Math.round((((phase % 1) + 1) % 1) * HERO_FRAMES * 1000) / 1000, u;
  const part = (n) => { u = (f + 1) / (n + 1); f -= n; return f < 0; };
  const run = (first, n) => ({ from: first + Math.floor(f + n), to: first, mix: 0, lift: 0 });                        // one capture a frame
  const land = (last, settled, n) => ({ from: last, to: settled, mix: ease(Math.min(1, (f + n + 1) / 3)), lift: 0 });   // and three frames to come to rest
  if (part(L.hold)) return { from: 0, to: 0, mix: 0, lift: 1 };
  if (part(L.settle)) return { from: 0, to: 0, mix: 0, lift: 1 - ease(u) };
  if (part(L.back)) return run(1, L.back);
  if (part(L.calm)) return land(R, R + 1, L.calm);
  if (part(L.stop)) return run(R + 2, L.stop);
  if (part(L.two)) return land(2 * R + 1, 2 * R + 2, L.two);
  if (part(L.go)) return run(2 * R + 3, L.go);
  if (part(L.rest)) return land(3 * R + 2, 0, L.rest);
  if (part(L.lift)) return { from: 0, to: 0, mix: 0, lift: ease(u) };
  return { from: 0, to: 0, mix: 0, lift: 1 };
}
// Six windows and the headline. The four behind drift; the ledger and the phone hold still, so their text
// stays sharp. P places them: far left, far right, left, right, centre, phone.
const heroWindows = (n, P) => [
  WIN(n, 'policy', CROP.page, {}, { fog: n ? 0.58 : 0.38, lod: 1.2, defocus: 2.5, breathe: [16, 0.15], bezel: 8 }),
  WIN(n, 'proof', CROP.page, {}, { fog: n ? 0.58 : 0.38, lod: 1.2, defocus: 2.5, breathe: [12, 0.8], bezel: 8 }),
  WIN(n, 'shared', null, {}, { lod: 0.4, defocus: 0.8, fog: n ? 0.18 : 0.10, breathe: [12, 0.5], still: true }),
  WIN(n, 'recall', CROP.ledger, {}, { lod: 0.4, defocus: 0.8, fog: n ? 0.18 : 0.10, breathe: [12, 0.0] }),
  WIN(n, 'replay-00', CROP.centre, {}, { breathe: [10, 0.6], bezel: 11, glow: 0.6, still: true, seq: replayFrames(n) }),
  WIN(n, 'm-holds', [0, 0, 1170, 1840], {}, { keyAmt: n ? 0.45 : 0.6, bezel: 8, breathe: [9, 0.3], still: true }),
  { parent: 4, box: 'say', pad: [24, 18], lift: 110, breathe: [12, 0.85], bezel: 8, radius: 14, ...LIFT(n), align: 0.6, glow: 0.3, glowColor: n ? '#7a3320' : undefined, grow: 1.14, shift: [64, 0] },
].map((w, i) => ({ ...w, ...(P[i] || {}) }));

// ---------- the numbers ----------
const FIGURES = NUMBERS.figures.map((f) => [f.value, f.label]);

export const LAYOUTS = {
  hero: {
    css: [840, 560], fade: [40, 40, 30, 46], cam: [11, 4], motion: 0.75, pool: [0.5, 0.42, 0.62, 0.62],
    play: heroPlay, frames: HERO_FRAMES, driftEvery: 2,
    night: HERO_NIGHT, day: HERO_DAY,
    windows: (n) => heroWindows(n, [
      { x: 104, y: 204, w: 116, z: -820, rot: [10, 18, 0] }, { x: 738, y: 204, w: 112, z: -820, rot: [10, -18, 0] },
      { x: 124, y: 330, w: 190, z: -320, rot: [2, 32, 0] }, { x: 706, y: 318, w: 206, z: -320, rot: [2, -32, 0] },
      { x: 404, y: 348, w: 400, z: 40, rot: [3, 0, 0] }, { x: 682, y: 412, h: 228, z: 170, rot: [0, -14, 0] },
    ]),
    type: [
      T('name', 'Holdfast', 420, 34, { fontSize: '60px' }, 'center'),
      T('line', 'A memory for coding agents in which every fact carries a check that proves it.', 420, 110, { fontSize: '16.5px' }, 'center'),
    ],
  },
  'hero-phone': {
    css: [381, 640], dpr: 1080 / 381, fade: [16, 16, 24, 30], cam: [7, 3], motion: 0.75, pool: [0.5, 0.40, 0.75, 0.55],
    play: heroPlay, frames: HERO_FRAMES, driftEvery: 2,
    night: HERO_NIGHT, day: HERO_DAY,
    windows: (n) => heroWindows(n, [
      { x: 90, y: 228, w: 92, z: -820, rot: [10, 18, 0] }, { x: 294, y: 226, w: 90, z: -820, rot: [10, -18, 0] },
      { x: 70, y: 318, w: 130, z: -320, rot: [2, 32, 0] }, { x: 314, y: 306, w: 136, z: -320, rot: [2, -32, 0], still: true },
      { x: 172, y: 396, w: 276, z: 40, rot: [3, 0, 0], crop: [504, 96, 1024, 1100] }, { x: 304, y: 472, h: 196, z: 170, rot: [0, -14, 0] },
      { grow: 1.0, shift: [0, 0], pad: [16, 14], lift: 96 },
    ]),
    type: [
      T('name', 'Holdfast', 190.5, 34, { fontSize: '52px' }, 'center'),
      T('line', 'A memory for coding agents in which<br>every fact carries a check that proves it.', 190.5, 102, { fontSize: '14.5px', textAlign: 'center' }, 'center'),
    ],
  },
  numbers: {
    css: [840, 168], fade: [30, 30, 20, 20], cam: [6, 2], pool: [0.5, 0.5, 0.75, 1.4],
    night: merge(NIGHT, { env: { glows: [[0.5, 0.5, 0.5, 0.045, GREEN]] }, motes: { n: 12 } }), day: merge(DAY, { motes: { n: 8 } }),
    windows: () => [{ x: 420, y: 84, w: 776, crop: [0, 0, 776, 100], z: 0, rot: [0, 0, 0], bar: false, bezel: 12, radius: 26, breathe: [4, 0], glow: 0.0, shadowOff: [0, -10, 36] }],
    type: FIGURES.map(([v, l], i) => T('big', `<b style="font-size:42px">${v}</b><span style="font-size:13px;margin-top:9px">${l}</span>`, 60 + i * 150, 40)),
  },
  // a phone gets the five as a list: the figure, then what it counts, one to a line
  'numbers-phone': {
    css: [381, 388], dpr: 1080 / 381, fade: [16, 16, 18, 18], cam: [4, 2], pool: [0.5, 0.5, 0.8, 1.2],
    night: merge(NIGHT, { env: { glows: [[0.5, 0.5, 0.6, 0.045, GREEN]] }, motes: { n: 12 } }), day: merge(DAY, { motes: { n: 8 } }),
    windows: () => [{ x: 190.5, y: 194, w: 337, crop: [0, 0, 337, 324], z: 0, rot: [0, 0, 0], bar: false, bezel: 12, radius: 24, breathe: [3, 0], glow: 0.0, shadowOff: [0, -10, 30] }],
    type: FIGURES.flatMap(([v, l], i) => [
      ...(i ? [T('rule', '', 44, 57 + i * 60 - 13, { width: '293px' })] : []),
      T('big', `<b style="font-size:34px">${v}</b>`, 44, 57 + i * 60),
      T('big', `<span style="font-size:14.5px">${l}</span>`, 172, 57 + i * 60 + 11),
    ]),
  },
  // the plate for the repository's social preview, 1280 x 640
  social: {
    css: [640, 320], fade: [26, 26, 22, 26], cam: [0, 0], motion: 0, pool: [0.70, 0.52, 0.42, 0.78],
    night: merge(NIGHT, lit(CORAL)), day: DAY,
    windows: (n) => [
      WIN(n, 'replay-00', CROP.centre, { x: 448, y: 150, w: 310, z: -80, rot: [2, -18, 0] }, { breathe: [0, 0], bezel: 9 }),
      { parent: 0, box: 'say', pad: [24, 18], lift: 76, breathe: [0, 0], bezel: 8, radius: 14, ...LIFT(n), glow: 0.3, glowColor: n ? '#7a3320' : undefined, grow: 1.02, align: 0, shift: [-26, 0] },
    ],
    type: [
      T('kick', 'A memory for coding agents', 40, 104, { fontSize: '10px' }),
      T('name', 'Holdfast', 38, 122, { fontSize: '46px' }),
      T('line', 'Every fact carries a check that proves it.', 40, 182, { fontSize: '15.5px', whiteSpace: 'normal', width: '236px' }),
    ],
  },
};
for (const [name, o] of Object.entries(PANELS)) { LAYOUTS[name] = panel(o); LAYOUTS[`${name}-phone`] = panelPhone(o); }
for (const k in LAYOUTS) LAYOUTS[k].dpr = LAYOUTS[k].dpr || 2;
