# How it works

This page is the model behind Holdfast: what a fact, a check and a piece of proof are, the five statuses and how a fact moves between them, notes and their time to live, replacing versus retracting, what `recall` does step by step, the time budgets, the two files on disk and why only one of them is committed, merging in git, compaction, and what keeps the local web page safe. The check language is in [Checks](checks.md), the rules for what a check may touch are in [Policy](policy.md), connecting an agent is in [Agents](agents.md), and the short tour is in the [README](../README.md).

## A fact, a check, proof

A **fact** is one plain sentence about a project or a machine, stored with who said it and when.

A **check** is one line that says how to test the sentence: a file contains some text, a port accepts connections, a command exits 0. There are ten kinds; see [Checks](checks.md).

**Proof** is what one run of the check saw: a status, one line of detail, the time, and how long the run took. Holdfast also calls it evidence.

```console
$ holdfast add "Postgres listens on port 5432" --check "port localhost:5432" --tag db
Stored, and proved just now.
HOLDS     f-9e5ar5  Postgres listens on port 5432
          proof: localhost:5432 accepts connections · checked just now
```

The check ran before the fact was stored. A fact whose check fails is refused, so every fact with a check was true at least once. From then on the check runs again whenever the fact is recalled, and the fact is shown with the proof of that run.

What a fact holds:

| Part | Rules |
| --- | --- |
| id | `f-` and six characters, chosen by Holdfast, such as `f-9e5ar5` |
| claim | one line, up to 400 characters |
| tags | up to 8, each up to 32 characters: lower-case letters, digits, `-`, `_`, `.`, `/` |
| check | optional; one check, stored in parts, not as the line you typed |
| by | who stored it: you on the command line, the agent's name over MCP |
| time to live | only for a fact without a check; 30 days unless you say otherwise |
| replaces | optional; the id of the fact this one supersedes |

A fact is never edited. To change one, [replace it](#replace-or-retract).

## The five statuses

A fact has exactly one status at any moment.

| Status | The fact has a check | Meaning |
| --- | --- | --- |
| `holds` | yes | The check passed the last time it ran. |
| `broken` | yes | The check ran and failed. The fact was true once and is not now. |
| `blocked` | yes | The check did not run: the policy does not allow it, it could not be run, or it has never run on this machine. Neither proved nor disproved. |
| `unproven` | no | A note. Nothing proves it. |
| `stale` | no | A note that is past its time to live. |

Three more labels appear in output. They are not statuses:

| Label | Meaning |
| --- | --- |
| `TIMED OUT` | The fact held when last checked, and this time the check did not finish inside the [time budget](#time-budgets). It is not proved. |
| `RETRACTED` | Withdrawn by someone, with a reason. Never recalled again. |
| `REPLACED` | Withdrawn in favour of a newer fact. Never recalled again. |

### How a fact moves between them

A fact with a check changes status only when its check runs. A note changes status only with time, or when a person vouches for it.

| From | To | What causes it |
| --- | --- | --- |
| (new, with a check) | `holds` | the check passes when the fact is stored |
| (new, with a check) | not stored | the check fails when the fact is stored |
| (new, with a check) | `blocked` | the policy does not allow the check; the fact is stored and waits for a person (an `unchanged` check is refused instead, because it needs the file's digest at that moment) |
| `holds` | `broken` | a later run of the check fails |
| `broken` | `holds` | a later run passes again |
| `holds` or `broken` | `blocked` | the policy no longer allows the check, or the check cannot be run |
| `blocked` | `holds` or `broken` | a person allows the check, and it runs |
| (new, no check) | `unproven` | a note is stored |
| `unproven` | `stale` | the note's time to live runs out |
| `stale` | `unproven` | a person runs `holdfast vouch` |
| any | retracted or replaced | `retract`, or a new fact that names it in `replaces` |

Holdfast never drops a broken fact by itself. It stays in view, at the top of `holdfast list` and `holdfast brief`, until someone replaces or retracts it, or the check passes again.

`holdfast why <id>` shows a fact's changes of status. Here the database was stopped and started again:

```console
$ holdfast why f-9e5ar5
HOLDS     f-9e5ar5  Postgres listens on port 5432
          proof: localhost:5432 accepts connections · checked just now
          check: port localhost:5432
          stored 25m ago by alex · tags: db

History
  25m ago   holds    localhost:5432 accepts connections
  9s ago    broken   localhost:5432 does not accept connections: connection refused
  just now  holds    localhost:5432 accepts connections
```

The history keeps changes of status, not every run, and at most the last 40.

## Notes

A fact without a check is a note. Nothing proves it, so Holdfast gives it a time to live in place of proof. The default is 30 days. `--ttl` (the `ttl` argument over MCP) sets another: a whole number and a unit, `s`, `m`, `h`, `d` or `w`, up to ten years in all.

```console
$ holdfast add "Staging is deployed from the release branch" --ttl 3s
Stored as a note. It has no check, so it will go stale.
UNPROVEN  f-3ydmvs  Staging is deployed from the release branch
          a note by alex, under a minute old; it has no check, so nothing proves it
```

Past its time to live, the note is `stale`. It is still recalled, with a plain warning:

```console
$ holdfast list
1 fact: 1 stale.

STALE     f-3ydmvs  Staging is deployed from the release branch
          a note by alex, under a minute old, past its 3s life; nothing proves it, so treat it as a guess
```

`holdfast vouch <id>` says "I looked, and this is still so". The time to live starts again from that moment.

```console
$ holdfast vouch f-3ydmvs
Vouched for. Its time to live starts again from now.
UNPROVEN  f-3ydmvs  Staging is deployed from the release branch
          a note by alex, confirmed by alex, under a minute old; it has no check, so nothing proves it
```

Vouching is for people. It is on the command line and on the web page, and there is no MCP tool for it, so an agent cannot keep its own notes fresh. Only a note can be vouched for:

```console
$ holdfast vouch f-9e5ar5
holdfast: f-9e5ar5 has a check, so it proves itself; there is nothing to vouch for
```

A note cannot be given a check later. Store the fact again with a check and name the note in `--replaces`:

```console
$ holdfast add "Staging is deployed from the release branch" --check 'contains deploy.toml "branch = \"release\""' --replaces f-3ydmvs
Stored, and proved just now.
HOLDS     f-j4nxgd  Staging is deployed from the release branch
          proof: deploy.toml contains 'branch = "release"' (line 1) · checked just now
```

## Replace or retract

Both withdraw a fact. Neither deletes anything: the fact stays in `facts.jsonl`, and `holdfast list --all` and `holdfast why` still show it.

| | Use it when | What is kept |
| --- | --- | --- |
| Replace: `holdfast add ... --replaces <id>`, or `remember` with `replaces` | the thing changed, and you know what is true now | the old fact, marked with the id of the new one |
| Retract: `holdfast retract <id> --reason "..."`, or the `retract` tool | the fact is wrong, or no longer matters | the old fact, with who withdrew it and why |

Replacing, after `pool = 20` became `pool = 25` in a configuration file:

```console
$ holdfast recall "connection pool"
1 fact: 1 broken.

BROKEN    f-p7maw5  The connection pool is 20
          proof failed: config/app.toml does not match "^pool = 20$" · checked just now · it last held 6m ago
          check: matches config/app.toml "^pool = 20$"

BROKEN: this was true once and is not now. Do not rely on it. If you find out what is true now, remember that with replaces set to the old id.
$ holdfast add "The connection pool is 25" --check 'matches config/app.toml "^pool = 25$"' --tag db --tag config --replaces f-p7maw5
Stored, and proved just now.
HOLDS     f-wsrfds  The connection pool is 25
          proof: config/app.toml matches "^pool = 25$" (line 6) · checked just now
$ holdfast why f-p7maw5
REPLACED  f-p7maw5  The connection pool is 20
          replaced by f-wsrfds
          check: matches config/app.toml "^pool = 20$"
          stored 12m ago by alex · tags: db, config
          replaces f-ypa3pb

History
  12m ago   holds    config/app.toml matches "^pool = 20$" (line 6)
  just now  broken   config/app.toml does not match "^pool = 20$"
```

A fact with a check takes another's place only once that check has run and passed. If the check fails, or the policy does not allow it to run, the replacement is refused and the old fact stays as it was. A note has no check to wait for, so a note replaces a fact at once, and shows as UNPROVEN.

Retracting needs a reason, and the reason is kept:

```console
$ holdfast retract f-ywh8qq
holdfast: say why f-ywh8qq is being retracted, in a few words: the reason is kept with it
$ holdfast retract f-ywh8qq --reason "the metrics exporter now listens on 9090"
RETRACTED f-ywh8qq  Nothing listens on port 9090
          retracted just now by alex: the metrics exporter now listens on 9090
```

A retraction is final. There is no command that brings a retracted fact back; store it again.

## What recall does

`holdfast recall "<words>"` and the `recall` tool do the same three things.

### 1. Match

The query is cut into words: runs of letters and digits, in lower case. Common words (`the`, `is`, `what`, `how`, `on` and the like) and single letters are dropped. Each word that is left scores against each live fact:

| The query word | Points |
| --- | --- |
| is a word of the claim, or is a tag | 3 |
| is the start of a word of the claim, or the other way round, as `port` and `ports` (a three-letter word only within two letters) | 2 |
| appears somewhere in the text of the check | 1 |

A fact's score is the sum over the query's words. A fact with a score of 0 is not returned. An empty query matches every fact.

The matches are ordered by score, then by how urgent their last known status is, then newest first. The list is cut to the limit: 8 by default, 20 for an empty query, and at most 50 (`-n` on the command line, `limit` over MCP).

This is word matching, not a search by meaning. `database` does not find a fact that says only `Postgres`. Tags are the way to make a fact findable under the words people will use.

### 2. Prove again

Every fact in the cut list that has a check is checked again, up to 8 at a time.

- A check that only reads files runs every time.
- A network or command check is not repeated if its last answer is less than 30 seconds old. A server that stays running (`holdfast mcp`, `holdfast serve`) has every answer it got. A single `holdfast recall` command has only what is in `evidence.jsonl`, so it runs most checks again.
- A blocked fact is tried again every time, in case a person has allowed it since.
- A check that does not finish inside the [time budget](#time-budgets) is thrown away, and the fact comes back as last known, marked as not proved.

The second of two recalls sent to one `holdfast mcp` server, 14 seconds apart:

```text
4 facts: 1 blocked, 3 hold.

HOLDS     f-4tv3sz  The health endpoint answers 200 with status ok
          proof: http://localhost:8080/health answered 200 and the body contains '"status":"ok"' · checked 14s ago
HOLDS     f-r8k2kk  The API listens on 127.0.0.1:8080
          proof: config/app.toml contains 'listen = "127.0.0.1:8080"' (line 2) · checked just now
HOLDS     f-9e5ar5  Postgres listens on port 5432
          proof: localhost:5432 accepts connections · checked 14s ago
BLOCKED   f-2bdk3r  The registry answers on 192.168.1.20
          not checked: the policy does not allow connections to 192.168.1.20. A person can allow it: holdfast allow net 192.168.1.20
          check: port 192.168.1.20:5000

BLOCKED: the check was not run, so this is neither proved nor disproved. Only a person can allow the check.
```

Each line says how old its proof is. `checked just now` means the check ran during this call.

### 3. Sort

The results are sorted again, now by their fresh status: best match first, and among equal matches `broken`, then `blocked`, `stale`, `unproven`, `holds`. In the example above the blocked fact is last because it matched the query on fewer words.

The head line counts the statuses. After the facts comes one line of advice for each label that needs one.

`recall` is the only command that matches. `holdfast check` and the `verify` tool take ids, or run everything. `holdfast list` and `holdfast why` run no checks and show each fact as last known.

## Time budgets

A check may wait on a network or a command, so every run has a limit, and every call has a limit for all its checks together.

| Limit | Value |
| --- | --- |
| One file, `http`, `port` or `dns` check | 5 seconds |
| One `cmd` or `says` check | 10 seconds |
| Checks running at once | 8 |
| All checks of one `recall` | 12 seconds |
| All checks of one `holdfast check` or `verify` | 90 seconds |
| All checks of one `holdfast brief` | 20 seconds |

A single check that runs out of its own time has an answer: a port that does not answer in 5 seconds is a failed check. See [Limits and timeouts](checks.md#limits-and-timeouts).

A call that runs out of its budget is different. The checks that had not finished are stopped, and what they might have said is thrown away. Those facts keep their last known status and are marked. A fact that held is shown as `TIMED OUT`, never as `HOLDS`:

```console
$ holdfast recall "tests pass" -n 9
9 facts: 1 timed out, 8 hold.

TIMED OUT f-7rr5yx  The auth tests pass
          not proved just now: the check did not finish in time · it held when last checked 45s ago
          check: cmd make test-auth
HOLDS     f-dzxa8n  The export tests pass
          proof: make test-export exited 0 · checked 5s ago
...
HOLDS     f-4zh7gj  The storage tests pass
          proof: make test-storage exited 0 · checked 5s ago

TIMED OUT: the check did not finish, so this is as last known and is not proved. Run verify on it again, or confirm it yourself before relying on it.
```

The head line counts such a fact as `timed out`, never under `hold`, and among equally relevant facts it is listed ahead of the ones that hold. Written as JSON, its status is `timed_out`. A fact that was already broken, blocked or unproven keeps its own label when its check does not finish, and the head line then ends with how many could not be checked again in time.

`holdfast check` exits 1 when any fact is broken or could not be checked in time, so a script cannot take a late check for a pass. `--strict` makes it exit 1 for a blocked fact too.

```console
$ holdfast check --quiet
88 facts: 16 timed out, 72 hold.

TIMED OUT f-4d2gdf  Suite 87 passes
          not proved just now: the check did not finish in time · it held when last checked 1m ago
          check: cmd make test-87
...
$ echo $?
1
```

The budgets are fixed. If a project has more slow checks than fit, check them by id in smaller groups (`holdfast check <id>...`), or make the checks cheaper: a file a build writes is quicker to test than the build.

## Two files

`holdfast init` creates a `.holdfast` folder in the project:

```console
$ holdfast init
Created ~/app/.holdfast

  facts.jsonl      the facts. Commit it, and everyone on the project shares them.
  evidence.jsonl   proof gathered on this machine. It stays out of git.
  policy           what checks may touch: this folder and this machine, until
                   you allow more (holdfast allow ...).
...
```

| File | In git | What it is |
| --- | --- | --- |
| `facts.jsonl` | committed | every fact, retraction and vouch, one JSON line each |
| `evidence.jsonl` | ignored | proof gathered on this machine, one JSON line each |
| `policy` | committed | what checks may touch; see [Policy](policy.md) |
| `.gitignore` | committed | keeps `evidence.jsonl` and `lock` out of git |
| `.gitattributes` | committed | tells git how to [merge](#merging-factsjsonl-in-git) `facts.jsonl` |
| `lock` | ignored | an empty file that processes lock while they write |

**A fact is shared. Proof is local.** "Postgres listens on port 5432" is a statement about the project that everyone on it should be able to recall. Whether it is true is a statement about one machine at one moment: your database may be up while a colleague's is down. Committing proof would put one machine's answers in front of everyone else, and would change a tracked file as checks run. So the claim travels and the proof does not.

Both files are journals. Lines are only ever added to them. Lines of `facts.jsonl`, with the times left out:

```json
{"t":"…","op":"add","id":"f-9e5ar5","fact":{"id":"f-9e5ar5","claim":"Postgres listens on port 5432","tags":["db"],"check":{"kind":"port","addr":"localhost:5432"},"by":"alex","created":"…"}}
{"t":"…","op":"add","id":"f-cs7whg","fact":{"id":"f-cs7whg","claim":"The connection pool is 30","tags":["db","config"],"check":{"kind":"matches","path":"config/app.toml","regex":"^pool = 30$"},"by":"example-client","created":"…","replaces":"f-wsrfds"}}
{"t":"…","op":"retract","id":"f-ywh8qq","by":"alex","reason":"the metrics exporter now listens on 9090"}
{"t":"…","op":"vouch","id":"f-ecsmpq","by":"alex"}
```

Lines of `evidence.jsonl`:

```json
{"t":"…","op":"evidence","id":"f-9e5ar5","ev":{"status":"holds","detail":"localhost:5432 accepts connections","at":"…","ms":0}}
{"t":"…","op":"evidence","id":"f-2bdk3r","ev":{"status":"blocked","detail":"the policy does not allow connections to 192.168.1.20. A person can allow it: holdfast allow net 192.168.1.20","fix":"holdfast allow net 192.168.1.20","at":"…","ms":0}}
```

Not every run of a check adds a line. Evidence is written when a fact's status changes, and otherwise at most once every five minutes for a check that keeps giving the same answer.

What follows from proof being local:

- On a machine that has the facts and no proof yet, such as a fresh clone, every fact with a check is `blocked` until its check has run there once. Nothing is assumed from another machine.

```console
$ holdfast list
2 facts: 2 blocked.

BLOCKED   f-93veb3  Port is 8080
          not checked: this check has not run on this machine yet
          check: port localhost:8080
BLOCKED   f-j4nxgd  Staging is deployed from the release branch
          not checked: this check has not run on this machine yet
          check: contains deploy.toml "branch = \"release\""

the policy file has changed and nobody has approved it on this machine, so only the defaults apply. A person can read .holdfast/policy and approve it: holdfast trust
$ holdfast check
Proved again just now. 2 facts: 2 hold.
...
```

- The last line of that listing is the policy's side of the same rule. It appears because this project's policy allows more than the defaults: such a policy does not apply until a person approves it on that machine. A project that never widened its policy shows no such line. See [Approval on each machine](policy.md#approval-on-each-machine).
- Deleting `evidence.jsonl` loses each fact's history and nothing else. The next `holdfast check` gathers proof again.
- A fact's history and its "it last held" time are this machine's. Two people can see different histories for the same fact, and both are right.

Writing is safe for several processes at once: an agent's server, the web page and your own commands can share one store. Each write is one appended block, written through to disk under a lock. A line that was cut short, or that is not valid JSON, is skipped with a warning, and the rest is read as usual:

```text
holdfast: 2 damaged journal lines were skipped; the rest of the memory is intact
```

A store holds up to 2000 live facts. Withdrawn facts do not count.

## Merging `facts.jsonl` in git

Two branches that both remember things both add lines to the end of `facts.jsonl`, which git would report as a conflict. `holdfast init` writes this line to `.holdfast/.gitattributes`:

```text
facts.jsonl merge=union
```

`union` is one of git's built-in merge drivers: where both sides changed the same place, it keeps the lines of both and marks no conflict (see [gitattributes](https://git-scm.com/docs/gitattributes)). That is correct for this file, because every line stands alone and the journal is only added to. Commit `.gitattributes` with the rest of `.holdfast`, or the rule does not reach your colleagues.

Holdfast reads a merged journal by these rules:

| In the merged file | What Holdfast does |
| --- | --- |
| facts added on both branches | keeps them all |
| the same line twice | reads it once |
| one fact retracted on both branches | keeps the first retraction in the file, with its reason |
| one fact replaced on both branches | keeps both new facts; the old one is marked as replaced by the first |
| a note vouched for on both branches | the later vouch counts |
| a line that is damaged | skips it, and says so |

Two limits:

- Holdfast matches facts by id, not by wording. If two people store the same fact on two branches, the merged store has it twice, under two ids. Retract one.
- Text that arrives with a pull is cleaned exactly as if it had just been typed: a claim, tag or name in a journal line is cut to the same limits, and a line whose check is not valid is dropped. A journal is still a file anyone with commit rights can write. Review changes to `.holdfast/facts.jsonl` and `.holdfast/policy` as you would review code.

## Compaction

`facts.jsonl` grows by one line for each fact, retraction and vouch, and is never rewritten.

`evidence.jsonl` grows faster: a line for each change of status, and a line every five minutes for each check that is being run and keeps holding. `holdfast compact` rewrites it to what still matters: each fact's changes of status, the last time it held, and its latest proof.

```console
$ holdfast compact
evidence.jsonl: 7.1 kB, was 9.1 kB. The facts themselves are never rewritten.
```

Nothing a command shows is lost by compacting. The new file is written beside the old one and swapped in whole, so a crash halfway leaves the old file in place. Other processes that have the store open notice the swap and read the file again.

Holdfast never compacts by itself. Run it when the file's size bothers you. A check that keeps holding adds at most twelve lines an hour, however often it runs.

## The web page

`holdfast serve` opens the ledger, a local page that shows every fact with its status, proof and history. From the page a person can add a fact, run checks, retract and vouch. The page shows the policy and has no way to change it. `holdfast serve --demo` shows a made-up project and touches no store.

```console
$ holdfast serve
The ledger is open at http://127.0.0.1:7878  (Ctrl+C to close it)
```

The page has no login. Its safety rests on four things.

### It listens on this machine only

The default address is `127.0.0.1:7878`. An address other machines can reach is refused:

```console
$ holdfast serve --addr 0.0.0.0:7878
holdfast: 0.0.0.0:7878 can be reached from other machines, and the ledger has no login. Use a 127.0.0.1 address, or pass --expose if that is what you want.
```

`--expose` lifts that refusal, and the check on the `Host` header below with it. Anyone who can reach the address can then read every fact and retract any of them. Use it only on a network where that is acceptable, or put something with a login in front of it.

### It answers only to its own name

A request whose `Host` header is not `localhost`, a name under `.localhost` or a loopback address is refused. This stops a page on another site from reaching the ledger through a DNS name it controls and points at your machine.

```console
$ curl -i -H 'Host: ledger.example.com' http://127.0.0.1:7878/
HTTP/1.1 403 Forbidden
...
holdfast answers on this machine only
```

### Calls need the `X-Holdfast` header

Every call that reads or changes facts is a `POST` to `/api/...` and must carry an `X-Holdfast` header. A browser does not let a page from another site add that header without asking the server first, and this server never agrees. If the request has an `Origin` header, it must also name the ledger's own address.

```console
$ curl -i -X POST -d '{"id":"f-9e5ar5","reason":"test"}' http://127.0.0.1:7878/api/retract
HTTP/1.1 403 Forbidden
...
{"ok":false,"error":"this call did not come from the Holdfast page"}
```

The answer is the same with the header and an `Origin` of another site. A request body is limited to 64 KiB.

### The page can load nothing from elsewhere

Every answer to a request the ledger accepts carries these headers:

```console
$ curl -i http://127.0.0.1:7878/
HTTP/1.1 200 OK
...
Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Content-Type: text/html; charset=utf-8
Cross-Origin-Resource-Policy: same-origin
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
...
```

| Header | Effect |
| --- | --- |
| `Content-Security-Policy` | Scripts, fonts and requests come from the ledger itself and nowhere else. Styles come from the ledger or from the page itself. Images come from the ledger or from `data:` addresses inside the page. No form may post anywhere, and no page may put the ledger in a frame. |
| `X-Frame-Options: DENY` | The same rule about frames, for older browsers. |
| `Cross-Origin-Resource-Policy: same-origin` | Other sites cannot load the ledger's files. |
| `Referrer-Policy: no-referrer` | A link out of the ledger does not say where it came from. |
| `X-Content-Type-Options: nosniff` | The browser does not guess file types. |

The page is built into the binary. It loads no fonts, scripts or analytics from the network.

### What this does not cover

These measures keep other websites and other machines out. They do not separate users of one machine: any program running as any user on the machine can open `127.0.0.1:7878` while the ledger is running, send the header, and add or retract facts. On a shared machine, close the ledger when you are done with it.
