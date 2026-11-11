# Changelog

## Unreleased

Nothing yet.

## 0.1.1

Three repairs to the ledger's page. Nothing changes on the command line or for an agent.

- **The headline keeps its word gaps.** The narrow type it is set in closed the gap after an r, so "no longer true" read as "no longertrue".
- **A hovered row is the page's own grey.** Pointing at a fact turned its row maroon in the dark theme and pink in the light one. The same fault put a faint red cast on every claim, most of all on a note that is fading, and on the placeholders of the form for a new fact. All three now keep the hue of the page.
- **The browser's own bar matches the page.** The colour the page hands the browser for its toolbar was a shade off the page's ground in both themes.

## 0.1.0

The first release.

- **Facts that carry their proof.** A fact is a claim and a check. The check runs when the fact is stored, and a claim whose check fails is refused. It runs again before the fact is recalled.
- **Ten kinds of check.** `file`, `contains`, `matches`, `unchanged`, `fresh`, `http`, `port`, `dns`, `cmd` and `says`, each of which can be turned around with `not`.
- **Five statuses.** HOLDS, BROKEN, BLOCKED, UNPROVEN and STALE. A check that did not finish is shown as TIMED OUT, never as HOLDS; as JSON its status is `timed_out`, and an export lists it under a heading of its own.
- **Notes.** A fact with no check is a note. It names who wrote it, goes stale after 30 days, and only a person can vouch for it.
- **A policy only a person widens.** Checks read the project folder and reach this machine by default. Every other folder, host and command needs a line a person wrote, and a policy that arrives with a clone does nothing beyond the defaults until it is approved on that machine. A blocked fact names the one command that would allow its check, in a field of its own (`fix`). That command is given whole or not at all: where the line would be too long, as with a file deep inside long folder names, it is the path in the reason that is shortened.
- **Four tools for the agent.** `remember`, `recall`, `verify` and `retract`, over the Model Context Protocol on stdio, with and without the `initialize` handshake.
- **A command line.** `add`, `list`, `check`, `recall`, `why`, `vouch`, `retract`, `brief`, `export`, `policy`, `allow`, `trust` and `compact`. `holdfast check` exits 1 when a fact is broken or could not be checked in time.
- **The ledger.** `holdfast serve` opens every fact, its proof and its history in a browser, on this machine only.
- **Shared facts, local proof.** `facts.jsonl` is committed and merges as a union; `evidence.jsonl` stays on the machine that gathered it.
- **A live demo.** The same engine compiled to WebAssembly, on a pretend machine you can change.
- **Binaries** for Linux and macOS, amd64 and arm64. No dependencies.
