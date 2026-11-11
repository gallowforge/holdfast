# Holdfast identity

**One idea:** a memory in which every fact carries a check that proves it.

## The product

- **Look:** a ledger. Blue-black like deep water (`#0a1014`), ruled with hairlines, with the state of the whole memory said in one sentence at the top. A light theme on cool paper (`#f4f7f9`) carries the same page for daylight.
- **Colour, 60/30/10:** blue-black dominant, near-white type (`#ebeff1`), and four state colours that are never used for anything but a state: green for what holds (`#56d1a3`), coral for what is broken (`#fb784f`), amber for what has gone stale (`#e1b75c`), violet for what the policy blocked (`#a7a5eb`). A fact with no check stays grey.
- **Type:** Archivo for everything you read, set tight in the headline. DM Mono for whatever came from the machine: evidence, checks, commands, ids and ages.
- **Voice:** plain and exact, the way a careful colleague reports. It says what is true and what to do about it: "3 facts are no longer true.", "Every check that ran holds.", "Not stored: the check fails right now, so this is not a fact yet.", "BROKEN: this was true once and is not now. Do not rely on it."
- **Signature details:**
  - The headline is a sentence that counts. Under it one bar shows the whole memory in proportion: broken, blocked, stale, unproven, holding.
  - A fact that stopped being true is struck through, with what its check found instead on the line below.
  - What needs a person comes first; what holds is at the bottom, each fact with its proof and its age.
  - Open a fact and it answers "how do you know?": the proof, the check with a Prove again button, the history, the record.
  - A blocked fact shows the one line a person would type to allow its check.
  - The demo has a rail of switches that change a pretend machine, so the ledger can be watched while facts stop being true.
  - The mark: a holdfast, the root a kelp grips the rock with.

## The README images

- **Glass:** the README joins the Glass family of the author's other repositories. The real pages and terminals float as frosted-glass windows in a blue-black room with coral low in the haze (dark), or on bright paper in sunlight (light). Every image ships dark and light, wide and phone, and the dark images show the page in its dark theme, so nothing glares.
- **Staging:** real captures of `holdfast serve --demo` and of command-line sessions run for real in a throwaway folder. Each feature panel lifts one element out of its screen: the answer a notes file gives beside the answer Holdfast gives, the claim that was refused, the proof of one fact, the reason a check was blocked, the check on a fresh clone.
- **Motion:** only the hero moves, in a loop at 12 frames a second. It opens on the finished picture, the ledger with its headline lifted out, "3 facts are no longer true.", and holds it. The headline settles back and the demo's own switches are pressed: the machine is put back and every check holds, the API is stopped and two facts break, Go is upgraded and it is three again, and the headline lifts out. The windows at the back drift slowly the whole time; the ledger, the terminal and the phone hold still, so their text stays sharp. The page's own animations are stepped by hand, so every build photographs the same moments.
- **Copy:** Geist in the images, set flush left beside the glass, with three true numbers under each panel. The band of five figures is drawn from `src/numbers.json`.
- **Source:** `make shots` builds the binary and runs `docs/readme/src/build.sh`, which captures the page and the terminals (`capture.cjs`) and renders every scene (`render.cjs`, `scene.js`, `layouts.js`); `loop.py` packs the hero's frames into its loop. `social.png` is the plate for the repository's social preview and for the demo's link card.
