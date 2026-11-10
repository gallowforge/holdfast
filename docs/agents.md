# Agents

Holdfast reaches an agent through the Model Context Protocol: the agent's client starts `holdfast mcp` and talks to it over standard input and output. This page shows how to connect Claude Code, Cursor, Codex CLI and any other MCP client, the flags of `holdfast mcp`, the protocol revisions it speaks, the instructions it gives the agent, the four tools and their arguments, and a paragraph to paste into `CLAUDE.md` or `AGENTS.md`. What a check is, is in [Checks](checks.md). What the agent may touch is in [Policy](policy.md). The model is in [How it works](how-it-works.md), and the short tour is in the [README](../README.md).

## Before you connect

1. Put `holdfast` on your `PATH`, or use its full path in the configurations below.
2. Run `holdfast init` in the project. This is optional: without a store, the first fact an agent remembers creates one (see [`--no-init`](#--no-init)).
3. Know which folder the server will run in. `holdfast mcp` uses the nearest `.holdfast` folder at or above its working directory. If your client starts servers somewhere else, name the project with `--dir <folder>` or the `HOLDFAST_DIR` variable.

The server is a local process. It opens no port and needs no account or key.

## Claude Code

Vendor documentation: <https://code.claude.com/docs/en/mcp>

Add the server from the project folder:

```console
$ claude mcp add holdfast -- holdfast mcp
```

Everything after `--` is the command Claude Code runs. By default the entry is stored for you alone, in this project. `--scope` changes that:

| Scope | Who gets it |
| --- | --- |
| `--scope local` (the default) | you, in this project |
| `--scope project` | everyone on the project: the entry is written to `.mcp.json` in the project root, which you commit |
| `--scope user` | you, in every project |

To share it with the team, either run `claude mcp add --scope project holdfast -- holdfast mcp` or write `.mcp.json` yourself:

```json
{
  "mcpServers": {
    "holdfast": {
      "command": "holdfast",
      "args": ["mcp"]
    }
  }
}
```

Claude Code asks each person to approve a server that comes from `.mcp.json` before it uses it.

To check the connection:

```console
$ claude mcp list
$ claude mcp get holdfast
```

Inside Claude Code, `/mcp` shows the server's status. `claude mcp remove holdfast` removes it.

## Cursor

Vendor documentation: <https://cursor.com/docs/mcp>

Create `.cursor/mcp.json` in the project:

```json
{
  "mcpServers": {
    "holdfast": {
      "type": "stdio",
      "command": "holdfast",
      "args": ["mcp", "--dir", "${workspaceFolder}"]
    }
  }
}
```

Cursor replaces `${workspaceFolder}` with the project root, the folder that contains `.cursor/mcp.json`. Its documentation does not say which folder a server is started in, so the example names the project with `--dir` and does not depend on it. The `command` must be on your system path, or be a full path.

Cursor also reads `~/.cursor/mcp.json`, for servers you want in every project. Holdfast is a memory for one project, so the project file is the right place for it.

Cursor asks for approval before it uses an MCP tool, by default.

## Codex CLI

Vendor documentation: <https://developers.openai.com/codex/mcp>

Add the server:

```console
$ codex mcp add holdfast -- holdfast mcp
```

Or write the table yourself, in `~/.codex/config.toml`, or in `.codex/config.toml` inside a project you have marked as trusted:

```toml
[mcp_servers.holdfast]
command = "holdfast"
args = ["mcp"]
```

`codex mcp list` shows the configured servers, and `/mcp` shows the active ones inside the Codex terminal interface.

Two optional keys of that table matter here. `cwd` sets the folder the server is started in. `tool_timeout_sec` is how long Codex waits for one tool call, 60 seconds by default; a `verify` of every fact may take up to 90 seconds (see [time budgets](how-it-works.md#time-budgets)), so raise it if you have many slow checks.

## Any other MCP client

Holdfast is a stdio server. Whatever the client's configuration looks like, it needs these three things:

| Setting | Value |
| --- | --- |
| Transport | stdio |
| Command | `holdfast` |
| Arguments | `mcp` |

Most clients take the `mcpServers` JSON shown for Claude Code above. If the client does not start the server in the project folder, add `--dir` and the folder to the arguments, or set `HOLDFAST_DIR` in the server's environment.

To see that it works, ask the agent what it remembers about the project. In a project with no facts, `recall` answers:

```text
Nothing is remembered here yet.
```

If you get that answer in a project that has facts, the server was started in another folder.

## `holdfast mcp`

```text
Usage: holdfast mcp [--as <name>] [--no-init]
  -as name
    	the name facts are stored under (default: what the client calls itself)
  -dir folder
    	the project folder (default: the nearest one with a .holdfast folder)
  -global
    	use the store for the whole machine, in your home folder
  -no-init
    	do not create a store when the first fact is remembered
```

### `--as`

Every fact records who stored it, and every retraction who withdrew it. The server takes the name from the first of these that is set:

1. `--as <name>`
2. the `HOLDFAST_AGENT` variable
3. the name the client gives for itself in the protocol

If none of them is set, the fact is stored without an author.

```json
{"command": "holdfast", "args": ["mcp", "--as", "claude"]}
```

```text
UNPROVEN  f-mxxb9b  Deploys go out from the release branch
          a note by claude, under a minute old; it has no check, so nothing proves it
```

Set it when the client's own name is not the one you want in the history, or when two agents share a project and you want to tell them apart. The name is a label. It is not a login, and nothing is decided by it.

### `--no-init`

Started in a folder with no store, the server still runs. `recall` answers that nothing is remembered, and the first `remember` creates `.holdfast` in the folder the server was started in, with the default policy, as `holdfast init` would.

With `--no-init` it creates nothing, and `remember` is refused:

```text
there is no Holdfast store in ~/app yet, and this server was told not to create one. A person can create it: holdfast init
```

Use it for a server you configure globally, so that an agent does not leave a `.holdfast` folder in every folder it is opened in.

### `--dir` and `--global`

`--dir <folder>` names the project, whatever folder the server is started in. `--global` serves the store in your home folder, for facts about the machine (see [the global store](policy.md#the-global-store)). To give an agent both, configure two servers with different names, for example `holdfast` and `holdfast-machine`.

## Protocol

The transport is stdio: JSON-RPC 2.0, one message per line, at most 4 MiB a message. Batches are refused. The server offers tools and nothing else: no resources, no prompts.

| Revision | How a session starts |
| --- | --- |
| `2026-07-28` | No handshake. Each request names the revision in its `_meta`, under `io.modelcontextprotocol/protocolVersion`. A client can ask what the server supports with `server/discover`. |
| `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05` | With `initialize`. The server answers with the revision the client asked for. If the client asks for one that is not in this list, the server answers `2025-11-25`. |

A client does not have to be told which to use. Each request is served the way it is written, so both kinds of client work against the same binary.

| Method | Answer |
| --- | --- |
| `initialize` | the revision, the server's name and version, and the [instructions](#what-the-server-tells-the-agent) |
| `server/discover` | the supported revisions, the server's name and version, and the instructions |
| `tools/list` | the four tools |
| `tools/call` | the tool's answer |
| `ping` | an empty result |
| `notifications/cancelled` | no answer; the call it names is stopped |

Things a client author should know:

- Every request is handled as it arrives, so answers to calls sent at once may come back in a different order. Match them by `id`.
- A request that names a revision the server does not speak gets error `-32022`, with the list it does speak:

```json
{"jsonrpc":"2.0","id":3,"error":{"code":-32022,"message":"unsupported protocol version","data":{"requested":"2030-01-01","supported":["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"]}}}
```

- A tool that does not exist is error `-32602` (`unknown tool: allow`). Any other method is `-32601`.
- A tool that runs and refuses, such as a `remember` whose check fails, is not a protocol error. It comes back as a normal result with `isError` set and the reason as text, so the agent can read it and correct itself.

## What the server tells the agent

A client that passes server instructions on to the model gives it this text, from `internal/mcp/tools.go`. It is the whole manual the agent gets, apart from the tool descriptions.

```text
Holdfast is this project's memory, and every fact in it carries its own proof.

Before you rely on something you believe about this project or machine (a port, a path, a version, a command, where something runs), call recall. Each fact with a check is proved again before it is returned, so what comes back marked HOLDS is true right now, and what comes back BROKEN used to be true and is not any more.

When you learn something a later session will need, call remember, and give it a check whenever the fact can be tested. A fact with a check cannot quietly go out of date. A fact without one is kept as a note and expires.

When a fact is wrong, call retract; when it has changed, call remember with replaces.
```

One qualification to "proved again before it is returned": `recall` reuses a network or command answer that is less than 30 seconds old, and a check that does not finish in time comes back as TIMED OUT, not HOLDS. See [how often a check runs](checks.md#how-often-a-check-runs).

## The four tools

Every tool answers with plain text, in the same form the command line prints, and with the same data as `structuredContent` for clients that read it. An argument that a tool does not list is refused.

In that data, and in what the command line writes with `--json`, each fact has a `status`: `holds`, `broken`, `blocked`, `unproven`, `stale`, or `timed_out` for a fact that held when it was last checked and whose check did not finish this time. A blocked fact also has `fix`, the one command that would allow its check. Read the command from that field and not out of `detail`: the detail quotes the check, and an agent wrote the check.

| Tool | What it does | Command-line twin |
| --- | --- | --- |
| [`remember`](#remember) | stores a fact, with its check | `holdfast add` |
| [`recall`](#recall) | returns the facts that match, proved again | `holdfast recall` |
| [`verify`](#verify) | runs checks again now | `holdfast check` |
| [`retract`](#retract) | withdraws a fact | `holdfast retract` |

There is no tool for the policy and none for `vouch`. Both belong to a person: see [what an agent can and cannot do](policy.md#what-an-agent-can-and-cannot-do).

The tools carry the protocol's hints for clients that use them. None of the four is marked read-only, and none is marked destructive. `recall` and `verify` change no fact, but they run checks, which may read files, connect to hosts and run the commands the policy allows, and they write down what the checks found.

### `remember`

| Argument | Type | Required | Meaning |
| --- | --- | --- | --- |
| `claim` | string | yes | The fact, as one plain sentence. At most 400 characters. |
| `check` | string | no | How to prove the claim, in the [check language](checks.md). |
| `tags` | array of strings | no | Up to 8 lower-case words to find the fact by. |
| `replaces` | string | no | The id of a fact this one supersedes. The old fact is withdrawn. |
| `ttl` | string | no | Only for a fact without a check: how long to believe it, such as `12h`, `7d` or `4w`. The default is `30d`. |

The check runs before anything is stored. What the agent reads back says which of four things happened:

```text
Stored, and proved just now.

HOLDS     f-ty467c  The module is atlas
          proof: go.mod contains "module example.com/atlas" (line 1) · checked just now
```

```text
the check fails right now, so this is not a fact yet: localhost:9090 does not accept connections: connection refused. Correct the claim or the check; or, to record that something is not so, start the check with "not".
```

```text
Stored, but not proved: the check may not run until a person allows it. Tell the person; until then treat this as unproven.

BLOCKED   f-sgz4k2  The test suite passes
          not checked: the policy does not allow the command: make test. A person can allow it: holdfast allow cmd make test
          check: cmd make test
```

```text
Stored as a note. It has no check, so nothing proves it and it will be marked stale when it is 30d old. Give facts a check whenever one is possible.

UNPROVEN  f-mxxb9b  Deploys go out from the release branch
          a note by example-client, under a minute old; it has no check, so nothing proves it
```

The second of these is a refusal: nothing was stored. Remembering the same claim with the same check twice stores nothing new and returns the fact that is already there.

A replacement is checked before it takes anything's place. If its check fails, or the policy does not allow the check to run, it is refused and the old fact stays as it was. When the check passes, the old fact is withdrawn in the same step:

```text
Stored, and proved just now. f-wsrfds was withdrawn in its favour.

HOLDS     f-cs7whg  The connection pool is 30
          proof: config/app.toml matches "^pool = 30$" (line 6) · checked just now
```

### `recall`

| Argument | Type | Required | Meaning |
| --- | --- | --- | --- |
| `query` | string | no | A few words about what is needed, such as `api port`. An empty query lists everything, most urgent first. |
| `limit` | integer, 1 to 50 | no | The most facts to return. The default is 8, or 20 for an empty query. |

```text
1 fact: 1 broken.

BROKEN    f-wsrfds  The connection pool is 25
          proof failed: config/app.toml does not match "^pool = 25$" · checked just now · it last held 3m ago
          check: matches config/app.toml "^pool = 25$"

BROKEN: this was true once and is not now. Do not rely on it. If you find out what is true now, remember that with replaces set to the old id.
```

After the facts come one line of advice for each label that needs one. The labels an agent can meet:

| Label | Meaning for the agent |
| --- | --- |
| `HOLDS` | The check passed. Rely on it. |
| `BROKEN` | It was true once and the check now fails. Do not rely on it. |
| `TIMED OUT` | The check did not finish, so the fact is as last known and is not proved. |
| `BLOCKED` | The check was not allowed to run. Neither proved nor disproved. |
| `UNPROVEN` | A note with no check. |
| `STALE` | A note with no check, past its time to live. |

Recall matches words. It is not a semantic search: a query finds a fact when its words appear in the claim, the tags or the check. How matching and ordering work is in [How it works](how-it-works.md#what-recall-does).

### `verify`

| Argument | Type | Required | Meaning |
| --- | --- | --- | --- |
| `ids` | array of strings | no | Fact ids such as `f-9e5ar5`. Leave out to prove every fact that has a check. |

```text
Proved again just now. 11 facts: 2 blocked, 9 hold.

BLOCKED   f-2bdk3r  The registry answers on 192.168.1.20
          not checked: the policy does not allow connections to 192.168.1.20. A person can allow it: holdfast allow net 192.168.1.20
          check: port 192.168.1.20:5000
...
HOLDS     f-cs7whg  The connection pool is 30
          proof: config/app.toml matches "^pool = 30$" (line 6) · checked just now
...
```

`verify` reuses nothing: every check named runs. It is the tool for an agent that has just changed something and wants to know what its change broke. Notes have no check and are not part of it.

### `retract`

| Argument | Type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | string | yes | The id of the fact. |
| `reason` | string | yes | Why, in a few words. It is kept with the fact. |

```text
Withdrawn. It will not be recalled again.

RETRACTED f-mafndg  Releases are cut on Thursdays
          retracted just now by example-client: releases are cut on demand now
```

A retracted fact stays in `facts.jsonl` with the reason and is never recalled again. When something has changed and is not gone, `remember` with `replaces` is the better call: the new fact takes the old one's place.

## A paragraph for CLAUDE.md or AGENTS.md

The server's instructions reach the agent only if the client passes them on, and an agent follows a rule in its own project file more reliably than a tool description. Paste this and adjust it:

```markdown
## Project memory (Holdfast)

This project has a memory you reach through the `holdfast` MCP tools. Every fact in it carries a check that proves it.

- Before you rely on something you believe about this project or machine (a port, a path, a version, a command, where something runs), call `recall` with a few words. Rely on what comes back as HOLDS. Do not rely on BROKEN. Treat TIMED OUT, BLOCKED, UNPROVEN and STALE as unconfirmed, and confirm them yourself.
- When you learn something a later session will need, call `remember` with a `check` that proves it. Prefer a file check (`contains`, `matches`) where one will do.
- After you change configuration, ports, versions or file layout, call `verify`. For each fact your change broke, call `remember` with the new fact and `replaces` set to the old id.
- When a fact is wrong and nothing replaces it, call `retract` with a reason.
- When a check comes back BLOCKED, tell me the `holdfast allow ...` line it names and carry on. Do not run that line yourself, and do not reword the check to get around the policy.
```

The last line is a request, not a lock. An agent with a shell is able to run `holdfast allow`; see [what this does not protect](policy.md#what-this-does-not-protect).

## Giving the agent a summary at the start

`holdfast brief` runs every check and prints a short summary meant for the start of a session: what is wrong first, then what holds. It prints nothing in a folder with no store, so it is safe in a hook that runs everywhere.

```console
$ holdfast brief
Holdfast, this project's memory: 12 facts: 2 blocked, 1 unproven, 9 hold. Every check was run just now.

BLOCKED   f-2bdk3r  The registry answers on 192.168.1.20
          not checked: the policy does not allow connections to 192.168.1.20. A person can allow it: holdfast allow net 192.168.1.20
          check: port 192.168.1.20:5000
...
UNPROVEN  f-ecsmpq  Staging is deployed from the release branch
          a note by alex, confirmed by alex, 12m old; it has no check, so nothing proves it

Proved and holding:
  f-9e5ar5  Postgres listens on port 5432
  f-4tv3sz  The health endpoint answers 200 with status ok
  f-cs7whg  The connection pool is 30
  ...

Before relying on any of this later in the session, ask again with the recall tool: it proves each fact again first.
```

How you feed it to the agent depends on the client: a session-start hook, or a line in your own start-up script. All its checks together get 20 seconds. `holdfast export` writes the whole memory as Markdown instead, without running checks unless you add `--check`.
