# Checks

A check is the one line that proves a fact. This page is the reference for the check language: the ten kinds of check, the `not` prefix, quoting, what each check proves and what it does not, the evidence Holdfast writes down, the limits and timeouts, how often a check runs, and what each check needs from the [policy](policy.md). For the model behind it, read [How it works](how-it-works.md). For the short tour, read the [README](../README.md).

## Writing a check

A check is one line: an optional `not`, the kind of check, and its arguments.

```text
[not] <kind> <argument>...
```

On the command line it goes in `--check`. An agent passes it as the `check` argument of the `remember` tool (see [Agents](agents.md)).

```console
$ holdfast add "Postgres listens on port 5432" --check "port localhost:5432" --tag db
Stored, and proved just now.
HOLDS     f-9e5ar5  Postgres listens on port 5432
          proof: localhost:5432 accepts connections · checked just now
```

The check runs before the fact is stored.

| The check | What happens |
| --- | --- |
| passes | The fact is stored as HOLDS. |
| fails | Nothing is stored. A claim that is false now is not a fact. |
| could not be run | The fact is stored as BLOCKED until a person [clears it](policy.md#blocked-and-how-to-clear-it). An `unchanged` check is refused instead: it has to read the file now. |

```console
$ holdfast add "The API listens on port 9090" --check "port localhost:9090"
Not stored: the check fails right now, so this is not a fact yet: localhost:9090 does not accept connections: connection refused. Correct the claim or the check; or, to record that something is not so, start the check with "not".
```

A check is never a script. It names one thing to look at and what must be seen there. `cmd` and `says` run a program, but without a shell, and only a command a person has allowed.

## The ten checks

| Check | Holds when | Policy line it needs | On recall |
| --- | --- | --- | --- |
| `file <path>` | the path exists | `files` | runs every time |
| `contains <path> <text>` | the file contains the text | `files` | runs every time |
| `matches <path> <regex>` | the file matches the regular expression | `files` | runs every time |
| `unchanged <path>` | the file is byte for byte what it was when the fact was stored | `files` | runs every time |
| `fresh <path> <duration>` | the file was modified within the duration | `files` | runs every time |
| `http <status> <url> [<text>]` | a GET answers with the status, and the body contains the text if one is given | `net` | answer reused for up to 30 seconds |
| `port <host:port>` | the port accepts a TCP connection | `net` | answer reused for up to 30 seconds |
| `dns <name> [<ip>]` | the name resolves, and one of its addresses is the IP if one is given | `net` | answer reused for up to 30 seconds |
| `cmd <program> [args...]` | the command exits 0 | `cmd` | answer reused for up to 30 seconds |
| `says <text> <program> [args...]` | the command exits 0 and its output contains the text | `cmd` | answer reused for up to 30 seconds |

`holdfast help` prints the same grammar.

## File checks

All five take a path. A relative path is read from the project root, which is the folder that holds `.holdfast`, wherever you run the command from. A path may also be absolute or start with `~/`. The path must lie inside a folder the policy allows, and a symbolic link that leads out of an allowed folder is refused.

A path that does not exist is a plain "no": the fact is BROKEN, or HOLDS under `not`. Anything that stops Holdfast from reading the file makes the fact BLOCKED instead: a folder where a file is needed, a file over the size limit, something that is not a regular file, a permission error, or a path outside the policy.

### `file <path>`

Holds when the path exists. A folder counts.

```text
proof: ../shared/schema.sql exists
proof: .env does not exist                    (under "not")
```

It proves that something is at that path. It says nothing about what is in it.

### `contains <path> <text>`

Holds when the file contains the text, byte for byte. The match is case-sensitive. The evidence names the line of the first occurrence.

```text
proof: config/app.toml contains 'listen = "127.0.0.1:8080"' (line 2)
proof failed: config/app.toml does not contain "workers = 8"
```

It proves the text is in the file. It does not prove where: `contains go.mod 1.2` is also true of `go 1.25` and of any dependency at `v1.2.0`. Use `matches` with anchors when position matters.

### `matches <path> <regex>`

Holds when the regular expression matches somewhere in the file. The syntax is RE2, as in Go's `regexp` package: no backreferences and no lookaround. `^` and `$` are the start and end of any line, as in `grep`; `\A` and `\z` are the start and end of the whole file. Start the expression with `(?i)` to ignore case. The expression may be up to 512 bytes long. The file is searched whole, not one line at a time, so an expression may run across a line break (`alpha\nbeta`).

```text
proof: go.mod matches "^go 1\.(2[5-9]|[3-9][0-9])$" (line 3)
proof failed: config/app.toml does not match "^pool = 10$"
```

A mistake in the expression is refused when the fact is stored:

```console
$ holdfast add "Go 1.25 or later" --check 'matches go.mod "go 1\.(2[5-9]"'
Not stored: matches: error parsing regexp: missing closing ): `go 1\.(2[5-9]`
```

So is an expression that would be slow to run. Holdfast compiles the expression and refuses one of more than 1000 steps, which nested counted repeats reach quickly:

```console
$ holdfast add "The module line is long" --check 'matches go.mod "(?:[a-z ]{1,30}){1,30}"'
Not stored: matches: the pattern is too complicated to run quickly; make it simpler
```

An expression that passes that test and still cannot finish a large file in the 5 seconds a check gets leaves the fact BLOCKED, not broken, because nothing was learned:

```console
$ holdfast add "The log never mentions a crash" --check 'not matches logs/app.log "a{1,400}z"'
Stored, but not proved: its check was not run.
BLOCKED   f-8dxd7t  The log never mentions a crash
          not checked: could not check: searching logs/app.log took too long
          check: not matches logs/app.log a{1,400}z
```

The first line of that answer is the same for every blocked check. The second line gives the real reason, and here it is not the policy.

### `unchanged <path>`

Holds while the file is byte for byte what it was when the fact was stored. Holdfast takes the file's SHA-256 at that moment and keeps it with the check in `facts.jsonl`. You never type the digest.

```text
proof: migrations/0001_users.sql is unchanged (sha256 09df4ce1…60b4)
proof failed: migrations/0001_users.sql has changed (sha256 was 09df4ce1…60b4, is now fb505883…6131)
proof failed: migrations/0001_users.sql does not exist any more
```

The file must exist, and the policy must allow reading it, when the fact is stored: the digest is taken at that moment. Where any other check outside the policy is stored as BLOCKED, an `unchanged` check is refused, because there would be nothing to compare the file with later. Because the digest travels with the fact, a teammate's check compares their copy of the file with the digest from the machine that stored it.

It proves the bytes are the same. Any edit breaks it, a comment or a trailing space included, so use it for files that must not be touched and not for facts about what a file says.

### `fresh <path> <duration>`

Holds when the file was last modified within the duration. A duration is a whole number and a unit: `s`, `m`, `h`, `d` or `w`, as in `90s`, `26h`, `7d`. The longest is ten years.

```text
proof: backups/latest.tar was modified 16s ago, within 26h
proof failed: logs/old.log was last modified 3d ago, more than 1d
```

It proves the file's modification time is recent. It does not prove the content is new: `touch` changes the time too.

## Network checks

All three name a host, and the policy must allow that host. See [what a check needs from the policy](#what-a-check-needs-from-the-policy).

`localhost` and any name ending in `.localhost` never go to a resolver. They always mean this machine: 127.0.0.1 and ::1.

### `http <status> <url> [<text>]`

Sends one GET and holds when the answer has exactly that status code. With a text, the body must also contain it, byte for byte.

```text
proof: http://localhost:8080/health answered 200 and the body contains '"status":"ok"'
proof: http://127.0.0.1:8080/missing answered 404
proof failed: http://localhost:8080/health answered 200, not 500
proof failed: http://127.0.0.1:8080/health answered 200, but the body does not contain "degraded"
proof: http://localhost:8081/health did not answer: connection refused      (under "not")
```

What the request is:

- The address must be `http` or `https`. The status must be a number from 100 to 599.
- The request carries `User-Agent: holdfast/<version>` and `Accept: */*`, and nothing else. There is no way to add a header, a body or a login.
- Redirects are followed, up to five, and every hop must be allowed by the policy. The status compared is the last answer's, so `http 301 ...` does not hold for a redirect that leads to a 200.
- With `https` the certificate must be valid, and TLS 1.2 is the minimum.
- No proxy is used, even when the environment names one.
- The body is read only when a text is given, and only its first 1 MiB.

An address with a user name or a password in it is refused:

```console
$ holdfast add "x" --check "http 200 https://alex:hunter2@example.com/"
Not stored: http: the address carries a user name or password. Holdfast stores checks in plain text; never put credentials in one
```

The same goes for the query string, although Holdfast cannot tell a token from a page number. The check is stored in `facts.jsonl`, which is meant to be committed, with the query string as written. Only the evidence hides it:

```text
proof: http://localhost:8080/health?… answered 200
```

### `port <host:port>`

Opens a TCP connection and closes it. Nothing is sent. Write an IPv6 address in brackets: `[::1]:8080`.

```text
proof: localhost:5432 accepts connections
proof: localhost:9090 does not accept connections: connection refused      (under "not")
```

It proves that something accepts connections on that port. It does not prove what. Use `http` when you care whether the service is healthy.

### `dns <name> [<ip>]`

Asks this machine's resolver for the name. It holds when the name resolves and, with an IP, when that IP is one of its addresses. The evidence lists up to four addresses.

```text
proof: localhost resolves to 127.0.0.1, ::1
proof failed: staging.example.com does not resolve: the name does not exist
```

A lookup sends the name to a DNS server. Holdfast only does that for a name the policy names, or when the policy opens `private` or `public` as a whole. Otherwise the fact is BLOCKED and nothing is looked up.

## Command checks

### `cmd <program> [args...]`

Runs the command and holds when it exits 0.

```text
proof: sleep 1 exited 0
proof failed: make test exited 2: make: *** No rule to make target 'test'. Stop.
```

How the command runs:

- There is no shell. The program is started with exactly the arguments given, so there are no pipes, redirections, wildcards or variables.
- It runs in the project root.
- Its environment is `PATH`, `HOME`, `LANG=C` and `LC_ALL=C`, and nothing else. Tokens and settings in your own environment do not reach it.
- It has 10 seconds. Then it is stopped, along with anything it started, and the answer is "no".
- The policy must allow the whole command, program and arguments, on a `cmd` line. No command is allowed by default.

It proves the exit code and nothing more. `cmd go version` only shows that Go runs.

### `says <text> <program> [args...]`

Runs the command in the same way and holds when it exits 0 and its output contains the text. The text comes first, then the command. Standard output and standard error are read together, up to the first 64 KiB.

```text
proof: ./bin/atlas --version printed "atlas 2.5"
proof failed: ./bin/atlas --version exited 0 but did not print "atlas 2.4" (it printed: atlas 2.5.0)
```

## The `not` prefix

Start any check with `not` to assert the opposite. The fact holds when the plain check would fail.

```console
$ holdfast add "There is no .env file in the repository" --check "not file .env" --tag config
Stored, and proved just now.
HOLDS     f-y5q8cb  There is no .env file in the repository
          proof: .env does not exist · checked just now

$ holdfast add "Nothing listens on port 9090" --check "not port localhost:9090" --tag api
Stored, and proved just now.
HOLDS     f-ywh8qq  Nothing listens on port 9090
          proof: localhost:9090 does not accept connections: connection refused · checked just now
```

`not` turns a "no" into a pass. It does not turn "could not be run" into anything: a BLOCKED check stays BLOCKED.

Every kind of "no" counts, so be exact about what you assert:

- `not contains config/production.toml debug` holds when the file has no such text, and also when the file does not exist. If the file must exist, store that as a second fact with `file`.
- `not port`, `not http` and `not dns` hold when nothing answers within 5 seconds, not only when the connection is refused.
- `not cmd` holds when the command exits with any other code, and also when it is stopped at 10 seconds.

## Quoting

Holdfast cuts the line into words the way a shell does, without running one.

| Written | Read as |
| --- | --- |
| words separated by spaces or tabs | one argument each |
| `"double quotes"` | one argument, spaces kept. `\"` is a double quote and `\\` is a backslash. Every other backslash is kept as written. |
| `'single quotes'` | one argument, everything kept as written. A single quote cannot appear inside. |

On the command line your shell reads the line before Holdfast does. The simplest habit is single quotes around the whole check and double quotes inside it:

```console
$ holdfast add "The API listens on 127.0.0.1:8080" --check 'contains config/app.toml "listen = \"127.0.0.1:8080\""' --tag api --tag config
Stored, and proved just now.
HOLDS     f-r8k2kk  The API listens on 127.0.0.1:8080
          proof: config/app.toml contains 'listen = "127.0.0.1:8080"' (line 2) · checked just now

$ holdfast add "Go 1.25 or later" --check 'matches go.mod "^go 1\.(2[5-9]|[3-9][0-9])$"'
Stored, and proved just now.
HOLDS     f-s5ezq6  Go 1.25 or later
          proof: go.mod matches "^go 1\.(2[5-9]|[3-9][0-9])$" (line 3) · checked just now
```

A check is one plain line of at most 1000 bytes. A line break or any other control character in an argument is refused.

## What evidence is recorded

Every run of a check produces one piece of evidence: a status (`holds`, `broken` or `blocked`), one line of detail, the time, and how long the check took in milliseconds. A blocked check also carries `fix`, the command a person can run to allow it, as a field of its own. It is written to `.holdfast/evidence.jsonl`, which stays on the machine that gathered it, when the fact's status changes, and otherwise at most once every five minutes for a check that keeps giving the same answer.

```json
{"t":"…","op":"evidence","id":"f-9e5ar5","ev":{"status":"holds","detail":"localhost:5432 accepts connections","at":"…","ms":0}}
```

The detail is at most 300 characters. It is built from:

- what the check itself names: the path, the address, the host, the command;
- the text or expression from the check, cut to 60 characters;
- what was measured: a line number, a shortened SHA-256, an age, a status code, an exit code, up to four resolved addresses;
- for a command that fails or does not print the expected text, the first line of its output, cut to 120 characters.

It never holds file content, a response body, response headers or a query string. A failed connection is described in a few fixed words (`connection refused`, `no answer within 5s`, `the TLS certificate was not accepted`), never with what the other side sent.

One limit to know. `contains` and `matches` answer a question about a file: is this text here, and on which line. Anyone who can propose checks can ask that question about any file the policy lets checks read, as often as they like. That is one bit at a time, but it is not nothing. The `files` lines of the policy decide which files can be asked about, so keep secrets outside the folders you allow.

## Limits and timeouts

| Limit | Value | At the limit |
| --- | --- | --- |
| Length of a check | 1000 bytes | refused when stored |
| Length of a `matches` expression | 512 bytes | refused when stored |
| Size of a compiled `matches` expression | 1000 steps | refused when stored |
| File read by `contains` and `matches` | 8 MiB | BLOCKED |
| File hashed by `unchanged` | 1 GiB | BLOCKED |
| Body read by `http` | first 1 MiB, and only when a text is given | the rest is not searched |
| Redirects followed by `http` | 5 | the answer is "no" |
| Output kept from `cmd` and `says` | first 64 KiB | the rest is dropped |
| Time for a file, `http`, `port` or `dns` check | 5 seconds | the answer is "no" for a network check; BLOCKED for a file that takes too long to search or hash |
| Time for a `cmd` or `says` check | 10 seconds | the command is stopped and the answer is "no" |
| Checks running at once | 8 | the others wait |
| Time for one recall, all checks together | 12 seconds | what is left is shown as TIMED OUT |
| Time for one `holdfast check` or `verify`, all checks together | 90 seconds | what is left is shown as TIMED OUT |
| Time for one `holdfast brief`, all checks together | 20 seconds | what is left is shown as TIMED OUT |

```console
$ holdfast add "The big log mentions startup" --check "contains logs/big.log startup"
Stored, but not proved: its check was not run.
BLOCKED   f-s5qzfd  The big log mentions startup
          not checked: logs/big.log is larger than 8 MiB; Holdfast does not scan files that large
          check: contains logs/big.log startup

$ holdfast add "The tests finish" --check "cmd sleep 20"
Not stored: the check fails right now, so this is not a fact yet: sleep 20 did not finish within 10s. Correct the claim or the check; or, to record that something is not so, start the check with "not".
```

The totals and the TIMED OUT label are explained in [How it works](how-it-works.md#time-budgets).

## How often a check runs

| When | Which checks run |
| --- | --- |
| The fact is stored | its check, once |
| `holdfast recall`, the `recall` tool | every matching fact's check, with one saving: a network or command answer less than 30 seconds old is reused |
| `holdfast check`, the `verify` tool, `holdfast export --check` | every check, always |
| `holdfast brief` | every check, always, with 20 seconds for all of them together |
| `holdfast list`, `holdfast why`, `holdfast export` | none; they show each fact as last known |

File checks cost almost nothing, so recall runs them every time. A network or command check waits on something outside Holdfast, so recall does not repeat one that answered within the last 30 seconds; the evidence line says so with `checked 14s ago` in place of `checked just now`. A BLOCKED fact is tried again on every recall, in case a person has allowed it since.

What counts is the age of the last answer, wherever it came from. A process that stays running (`holdfast mcp`, `holdfast serve`) has every answer it got. A single `holdfast recall` command knows only what is in `evidence.jsonl`, which is written less often than checks run (see [What evidence is recorded](#what-evidence-is-recorded)), so it runs most checks again.

## What a check needs from the policy

| Check | Needs | Allowed by default |
| --- | --- | --- |
| `file`, `contains`, `matches`, `unchanged`, `fresh` | the path inside a folder on a `files` line | the project folder |
| `http`, `port`, `dns` | the host allowed on a `net` line | this machine (`loopback`) |
| `cmd`, `says` | the whole command matched by a `cmd` line | nothing |

A check outside the policy is not run. The fact is stored as BLOCKED, and the evidence says what a person would have to allow:

```console
$ holdfast add "The toolchain is Go 1.25 or later" --check "says go1. go version"
Stored, but not proved: its check was not run.
BLOCKED   f-qv88b5  The toolchain is Go 1.25 or later
          not checked: the policy does not allow the command: go version. A person can allow it: holdfast allow cmd go version
          check: says go1. go version
```

An `unchanged` check is the one exception: it takes the file's digest when the fact is stored, so a file it may not read then is refused outright.

The grammar of the policy, and how a person widens it, is in [Policy](policy.md).

## Choosing a check

A good check fails on the day the claim stops being true, and not before.

| Claim | Weak check | Better check | Why |
| --- | --- | --- | --- |
| The connection pool is 20 | `file config/app.toml` | `matches config/app.toml "^pool = 20$"` | The weak check holds whatever the pool is. |
| The project needs Go 1.25 | `contains go.mod 1.25` | `matches go.mod "^go 1\.25$"` | The text `1.25` can also sit in a dependency's version. |
| The API is up on port 8080 | `port localhost:8080` | `http 200 http://localhost:8080/health` | A port only shows that something listens. |
| The toolchain is Go 1.25 | `cmd go version` | `says go1.25 go version` | The exit code does not carry the version. |
| The users migration is final | `file migrations/0001_users.sql` | `unchanged migrations/0001_users.sql` | The file can exist and still have been edited. |
| The backup runs every night | `file backups/latest.tar` | `fresh backups/latest.tar 26h` | An old backup still exists. |
| Secrets are not committed | none | `not file .env` | An absence can be checked too. |

Some habits that pay off:

1. Prefer a file check when one will do. It needs no approval inside the project, and recall runs it every time.
2. Say which thing you mean. `contains config/app.toml 'listen = "127.0.0.1:8080"'` proves what the configuration says. `port localhost:8080` proves what is running. They can disagree, and they are two facts.
3. One fact, one check. Split "the API is on 8080 and the database on 5432" in two.
4. Keep commands short. A check has 10 seconds, so a test suite that takes a minute is not a check.
5. Do not use `cmd` to read a file (`cmd grep ...`, `says ... cat ...`). `contains` and `matches` do it without a `cmd` line in the policy.
6. Put nothing secret in a check. It is stored in plain text in a file meant to be committed.
7. Remember that a shared fact is proved on each machine. `port localhost:5432` is about whichever machine runs the check.

## When a check is refused

A check that cannot be read is refused before anything runs, and nothing is stored. The messages say what to write instead.

| Written | Answer |
| --- | --- |
| `exists go.mod` | `unknown check "exists". A check starts with one of: file, contains, matches, unchanged, fresh, http, port, dns, cmd, says, optionally after "not"` |
| `contains go.mod` | `contains takes 2 arguments: contains <path> <text>  (quote the text if it has spaces)` |
| `contains go.mod "unterminated` | `a double quote is never closed` |
| `http ok https://example.com/` | `http: "ok" is not a status code. Write: http 200 https://example.com/health` |
| `port localhost` | `port: "localhost" is not host:port` |
| `port example.com:99999` | `port: "99999" is not a port number` |
| `fresh logs/old.log soon` | `duration "soon": the unit must be s, m, h, d or w` |
| `fresh go.mod 1.5h` | `duration "1.5h": write a whole number and a unit, like 26h or 30d` |
| `unchanged missing.txt` | `cannot store this: missing.txt does not exist, so there is nothing to keep unchanged` |
| `not` | `the check is empty. A check starts with one of: file, contains, matches, unchanged, fresh, http, port, dns, cmd, says, optionally after "not"` |

`holdfast add` prints `Not stored:` before the message and exits 1.
