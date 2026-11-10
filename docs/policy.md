# Policy

The policy is the file that says what checks may touch: which folders they may read, which hosts they may connect to, and which commands they may run. This page covers its grammar, the defaults, the three commands that work on it (`holdfast policy`, `holdfast allow`, `holdfast trust`), why each machine approves a policy for itself, the store in the home folder, what an agent can and cannot do, and what BLOCKED means and how a person clears it. The checks themselves are in [Checks](checks.md), the model is in [How it works](how-it-works.md), and the short tour is in the [README](../README.md).

## What the policy decides

Anyone can propose a check, an agent included. The policy decides whether Holdfast runs it.

| A check | Needs |
| --- | --- |
| `file`, `contains`, `matches`, `unchanged`, `fresh` | the path inside a folder on a `files` line |
| `http`, `port`, `dns` | the host allowed on a `net` line |
| `cmd`, `says` | the whole command matched by a `cmd` line |

A check outside the policy is not run. Its fact is stored and shown as BLOCKED, with the line a person would have to add. An `unchanged` check is the one exception: it takes the file's digest when the fact is stored, so a file it may not read then is refused outright. Nothing an agent can reach through the MCP server changes the policy.

## The file

The policy is `.holdfast/policy`, a plain text file. `holdfast init` writes it:

```text
# Holdfast policy: what checks may touch.
#
# Agents can propose checks. Only a person widens this file, by editing it or
# with "holdfast allow". A check outside the policy is not run; its fact shows
# as blocked until a person decides.
#
#   files <folder>...     folders checks may read (relative to the project root)
#   net   <who>...        loopback, private, public, a host name (example.com,
#                         *.example.com), an address or a range (10.0.0.0/8)
#   cmd   <program> ...   one command checks may run; * stands for any one
#                         argument, ** at the end for any further arguments

files .
net loopback
```

It is meant to be committed with `facts.jsonl`, so a team shares one policy. Committing it does not put what it adds in force anywhere: a policy that allows more than the defaults waits on each machine until a person there [approves it](#approval-on-each-machine).

## Grammar

One rule per line. A line starts with `files`, `net` or `cmd`.

| Line | Allows | Values on one line |
| --- | --- | --- |
| `files <folder>...` | reading files in these folders, and in every folder below them | several |
| `net <who>...` | connecting to these hosts, and looking their names up | several |
| `cmd <program> [<argument>...]` | running exactly this command | one command |

- A blank line is skipped. A line that starts with `#` is a comment. There are no comments at the end of a rule.
- Words are cut the same way as in a check: spaces separate them, and single or double quotes keep a word with spaces together. See [Quoting](checks.md#quoting).
- The policy only ever adds. There is no line that forbids something; to take a permission away, delete its line.
- A control character anywhere in the file is refused, in a comment too, because it could hide what a line says when the file is shown. A tab is the one exception: it counts as a space.
- A mistake on any line puts the whole policy out of force, and no check may touch anything until it is fixed. See [A mistake in the file](#a-mistake-in-the-file).

### `files`

A folder is written from the project root (`.`, `config`, `../shared`), from the home folder (`~/notes`), or in full (`/srv/app`). The project root is the folder that holds `.holdfast`.

- A folder allows everything below it.
- A symbolic link that leads out of an allowed folder is not followed. The fact is BLOCKED.
- A folder that does not exist allows nothing, so a `files ../shared` line helps only on a machine that has that folder.

### `net`

| Written | Means |
| --- | --- |
| `loopback` | this machine: `127.0.0.0/8`, `::1`, `0.0.0.0`, `::`, and the names `localhost` and `*.localhost` |
| `private` | private addresses: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7` and `100.64.0.0/10` |
| `public` | every other address that can be connected to. It does not include loopback, private or link-local addresses. |
| `staging.example.com` | that host name, whatever address it resolves to |
| `*.example.com` | every name below `example.com`, at any depth. Not `example.com` itself. |
| `192.168.1.20` | that one address |
| `10.0.0.0/8` | every address in the range |

Rules that follow from the table:

- Host names are compared without regard to case, and a trailing dot is ignored.
- `localhost` and names ending in `.localhost` are never sent to a resolver. They always mean 127.0.0.1 and ::1.
- A name is looked up only when the policy lets it be. That is the case when a rule names it, or when the policy has `private` or `public`. An address or a range on its own never lets a name be looked up.
- The decision is made again on the address Holdfast actually connects to. A name that only `public` allows is refused if it resolves to a private address, and the other way round.
- A host a person named is allowed wherever it resolves, with one exception: link-local addresses.
- Link-local addresses (`169.254.0.0/16`, `fe80::/10`) are where cloud metadata services live. No word and no host name allows them. Only the address itself, or a range that holds it, does.
- Holdfast never connects to a multicast address or to `255.255.255.255`, whatever the policy says.
- For `http`, every redirect is checked against the policy as well.

```console
$ holdfast add "The metadata service answers" --check "http 200 http://169.254.169.254/latest/"
Stored, but not proved: its check was not run.
BLOCKED   f-t6fgrf  The metadata service answers
          not checked: 169.254.169.254 is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254
          check: http 200 http://169.254.169.254/latest/
```

### `cmd`

A `cmd` line is one whole command: the program and its arguments. A check may run only a command that a line matches from the first word to the last.

| In a rule | Matches |
| --- | --- |
| a plain word | exactly that word |
| `*` | any one argument |
| `**` at the end | no further arguments, or any number of them |

- The program is matched exactly, as written. `cmd go version` does not allow `./go version` or `/tmp/go version`.
- The program cannot be a wildcard. `**` may only be the last word.
- Without `**`, the number of arguments must be the same. `cmd go version` does not allow `go version -m`.
- No command is allowed by default.

```text
cmd go version                  only "go version"
cmd systemctl is-active *       "systemctl is-active" with exactly one more argument
cmd git -C . **                 "git -C ." followed by anything
```

Be careful with `**`. A rule such as `cmd git **` allows every `git` subcommand, and some of them change things or run other programs.

## Defaults

A new project store has two rules:

```text
files .
net loopback
```

That is: checks may read the project folder, and may connect to this machine. No command may run, and nothing outside the machine is contacted.

The same two rules are what apply when the policy file is missing, and when it is present but [not approved on this machine](#approval-on-each-machine). The [global store](#the-global-store) has a narrower default.

## Seeing the policy: `holdfast policy`

`holdfast policy` prints the path of the file, the file as written, and whether it is in force. It exits 0 when it is, and 1 when it is not.

```console
$ holdfast policy
~/app/.holdfast/policy

# Holdfast policy: what checks may touch.
...

files .
net loopback
cmd go version
files ../shared
net staging.example.com
cmd systemctl is-active *
cmd git -C . **
net *.example.com

In force: approved on this machine.
```

For a file that allows nothing beyond the defaults, the last line is `In force: it allows nothing beyond the defaults, so it needs no approval.` For one that is waiting, it is `Not in force:` and the reason.

## Widening it: `holdfast allow`

```text
holdfast allow net <who>... | cmd <program> [<argument>...] | files <folder>...
```

`holdfast allow` checks the rule, appends it to the policy file, approves the new file on this machine, and runs the check of every BLOCKED fact again.

```console
$ holdfast add "The toolchain is Go 1.25 or later" --check "says go1. go version"
Stored, but not proved: its check was not run.
BLOCKED   f-ah77bh  The toolchain is Go 1.25 or later
          not checked: the policy does not allow the command: go version. A person can allow it: holdfast allow cmd go version
          check: says go1. go version

$ holdfast allow cmd go version
Allowed: cmd go version

The facts that were blocked, checked again:
...
HOLDS     f-ah77bh  The toolchain is Go 1.25 or later
          proof: go version printed "go1." · checked just now
```

Allowing a check lets it run. It does not make it pass:

```console
$ holdfast allow net staging.example.com
Allowed: net staging.example.com

The facts that were blocked, checked again:
BROKEN    f-mfgekb  Staging answers
          proof failed: https://staging.example.com/health did not answer: the name does not exist · checked just now
          check: http 200 https://staging.example.com/health
...
```

Things to know:

- Quote `*` and `**` so your shell does not expand them: `holdfast allow cmd systemctl is-active '*'`.
- Flags go before the kind: `holdfast allow --global files ~/notes`. After it, they would become part of the rule, and Holdfast refuses.
- A rule that is already there is not added twice: `Already allowed: cmd go version`.
- `holdfast allow` will not add to a policy that is not approved on this machine. Read it and approve it first.

```console
$ holdfast allow cmd make test
holdfast: the policy file has changed since it was last approved on this machine, so it is not widened blind.
Read it with "holdfast policy", approve it with "holdfast trust", then allow more.
```

A rule that cannot be right is refused, and the file is left as it was:

| Command | Answer |
| --- | --- |
| `holdfast allow cmd '*'` | `a cmd rule must name its program; "*" would allow every command` |
| `holdfast allow cmd go '**' build` | `** may only end a cmd rule` |
| `holdfast allow net 'exa mple'` | `net rule "exa mple": write loopback, private, public, a host name, *.a-domain, an address or a range` |
| `holdfast allow web example.com` | `"web" is not something a policy allows; write net, cmd or files` |
| `holdfast allow files ''` | `an empty files rule: to allow the store's whole folder, write: files .` |

For a `cmd` rule the answer starts with the line the rule would have had in the file, as in `policy line 18: ** may only end a cmd rule`. Each of these exits 2.

You can also edit the file by hand. A hand edit is a change like any other: unless what is left allows nothing beyond the defaults, it needs [approving](#approval-on-each-machine) before it applies. To take a rule away, delete its line and run `holdfast trust`.

## Approval on each machine

A policy file that allows more than the [defaults](#defaults) applies on a machine only after a person on that machine has approved it. Until then only the defaults apply. A file that allows exactly the defaults has nothing in it to approve and is in force as it stands, so a project that never widened its policy works in a fresh clone with nothing to run first.

The reason is the clone. The policy is committed, so a repository you clone or pull brings its own policy with it, written by someone else. If that file applied by itself, opening the project with an agent would be enough to run whatever commands the file allows. So Holdfast treats a policy it has not seen approved here as a proposal.

A fresh clone of a project with a widened policy looks like this. No check has run on this machine, and the policy is waiting:

```console
$ holdfast list
7 facts: 7 blocked.

BLOCKED   f-ty467c  The module is atlas
          not checked: this check has not run on this machine yet
          check: contains go.mod "module example.com/atlas"
...
BLOCKED   f-ah77bh  The toolchain is Go 1.25 or later
          not checked: this check has not run on this machine yet
          check: says go1. go version

the policy file has changed and nobody has approved it on this machine, so only the defaults apply. A person can read .holdfast/policy and approve it: holdfast trust
```

Checks inside the defaults already run. The rest stay BLOCKED:

```console
$ holdfast check
Proved again just now. 7 facts: 6 blocked, 1 holds.

BLOCKED   f-ah77bh  The toolchain is Go 1.25 or later
          not checked: the policy does not allow the command: go version. A person can allow it: holdfast allow cmd go version
          check: says go1. go version
...
HOLDS     f-ty467c  The module is atlas
          proof: go.mod contains "module example.com/atlas" (line 1) · checked just now
```

### `holdfast trust`

`holdfast trust` lists what the file allows, in plain words, and asks.

```console
$ holdfast trust
~/app/.holdfast/policy allows checks to:

  read files in     .
  read files in     ../shared
  connect to        loopback
  connect to        staging.example.com
  connect to        *.example.com
  run the command   go version
  run the command   systemctl is-active *
  run the command   git -C . **

Approve this on this machine? [y/N] 
```

Answer `y` or `yes` and the policy is in force:

```text
Approved on this machine. A later change to the file will need approving again.
```

Any other answer leaves it as it was, and the command exits 1: `Not approved. Only the defaults apply.`

Outside a terminal there is nobody to ask, so `holdfast trust` prints the list, approves nothing and exits 1:

```text
holdfast: not approved. Read the list above, then run this in a terminal, or pass --yes.
```

`holdfast trust --yes` approves without asking. Use it in a script only for a policy you have read.

When there is nothing to do, `holdfast trust` says so and exits 0: `It is already approved on this machine.`, or, for a file that allows nothing beyond the defaults, `It allows nothing beyond the defaults, so there is nothing to approve.`

### What an approval is

An approval is one line in a file outside every project: the SHA-256 of the policy text, and the path of the store.

| System | The approvals file |
| --- | --- |
| Linux | `~/.config/holdfast/approved` (or under `$XDG_CONFIG_HOME`) |
| macOS | `~/Library/Application Support/holdfast/approved` |
| anywhere, when `HOLDFAST_STATE` is set | `$HOLDFAST_STATE/approved` |

Because it lives outside the repository, a repository cannot bring its own approval along. What follows from the two parts of the line:

- Any change to a file that allows more than the defaults needs approving again: a pull that changes it, a hand edit, even a comment. `holdfast allow` approves its own change. A file that allows exactly the defaults needs no approval, however it is worded.
- Putting the file back to the text that was last approved puts it back in force, with no second approval. Only the last approval is kept: an older version of the file needs approving again.
- The approval belongs to the folder. Move or rename the project and the policy needs approving again.
- The approval belongs to the user account on that machine. Every other machine, and every other user, approves for itself.

While a changed policy waits for approval, every fact that needs more than the defaults goes back to BLOCKED the next time it is checked, a fact that held a moment ago included.

### A mistake in the file

A policy that cannot be read as written is not half applied. Nothing is allowed, not even the defaults, until it is fixed.

```console
$ holdfast policy
...
Not in force: the policy file has a mistake (policy line 15: unknown word "network" (expected files, net or cmd)), so no check may touch anything until it is fixed
```

`holdfast trust` refuses to approve it. Fix the line, then approve.

## The global store

`holdfast init --global` creates a store in your home folder, `~/.holdfast`, for facts about the machine and not about one project. Every command takes `--global` to work on it.

Its root is the home folder, which holds keys, tokens and mail. So its default policy allows no folder at all:

```text
net loopback
```

Name the folders that matter:

```console
$ holdfast allow --global files ~/dotfiles
Allowed: files ~/dotfiles
```

Everything else on this page applies to it unchanged. Its policy is `~/.holdfast/policy`, and it has its own approval.

## What an agent can and cannot do

Through the MCP server an agent has four tools: `remember`, `recall`, `verify` and `retract` (see [Agents](agents.md)).

| An agent can | An agent cannot |
| --- | --- |
| propose any check, on any path, host or command | make a check run that the policy does not allow |
| read the BLOCKED answer, which names what a person would have to allow | change the policy file, or approve it |
| ask again later, when a person may have allowed it | vouch for a note |

A proposed check that is outside the policy comes back like this, and the agent is told to pass it on:

```text
Stored, but not proved: the check may not run until a person allows it. Tell the person; until then treat this as unproven.

BLOCKED   f-sgz4k2  The test suite passes
          not checked: the policy does not allow the command: make test. A person can allow it: holdfast allow cmd make test
          check: cmd make test
```

### What this does not protect

Be clear about the edges.

- The policy protects the MCP tools. An agent that also has a shell can run any command directly, can run `holdfast allow`, and can edit `.holdfast/policy` and the approvals file. Holdfast cannot tell a person at a keyboard from a program that types. What the policy guarantees is that the memory itself is not a way around your agent's own permission system: a fact cannot carry a command that runs without a person having allowed it.
- Approval protects you from a policy someone else wrote. It does not protect you from approving one without reading it.
- `contains` and `matches` answer one question about a file: is this text in it. An agent can ask that about any file inside the folders on the `files` lines, as often as it likes. Keep secrets out of those folders. The project folder is allowed by default, so that includes an `.env` file in it.
- With `private` or `public` in the policy, any host name in a check is sent to your resolver, including a name that is then refused by its address.
- An allowed command runs with your user's rights. The policy matches words; it does not know what the program does with them.

## BLOCKED and how to clear it

BLOCKED means the check was not run to an answer, so the fact is neither proved nor disproved. Treat it as unknown. It is not a failure of the claim.

Holdfast tries a BLOCKED fact again on every recall and every `holdfast check`, so nothing has to be reset. Clear the cause and the next look proves it.

| The evidence line says | Cause | A person clears it with |
| --- | --- | --- |
| `this check has not run on this machine yet` | the fact arrived by a clone or a pull, and proof is local | `holdfast check` |
| `the policy does not allow the command: ...` | no `cmd` line matches | the `holdfast allow cmd ...` line printed with it |
| `the policy does not allow connections to ...` | no `net` line allows the host or its address | the `holdfast allow net ...` line printed with it |
| `... is outside the folders the policy allows` | no `files` line holds the path | the `holdfast allow files ...` line printed with it |
| `... is a link-local address ...` | link-local needs its address spelled out | `holdfast allow net <address>` |
| `... leads outside the folders the policy allows (through a symbolic link)` | a link points out of an allowed folder | nothing in the policy; name the real path in the check, and allow its folder |
| `Holdfast never connects to ...` | a multicast or broadcast address | nothing; change the check |
| `... is larger than 8 MiB; Holdfast does not scan files that large` | a [limit](checks.md#limits-and-timeouts) | nothing; check a smaller file, or use `file`, `fresh` or `unchanged` |
| `cannot read ...: permission denied` | the file is there, and your user may not read it | fixing the file's permissions |
| `could not check: ... could not be run: ...` | the command is allowed, and the program could not be started | installing the program, or correcting its name |

When the whole policy is out of force, a note under the output says so, and every fact outside the defaults is BLOCKED at once:

| The note says | A person clears it with |
| --- | --- |
| `the policy file has changed and nobody has approved it on this machine, so only the defaults apply` | `holdfast policy` to read it, then `holdfast trust` |
| `the policy file has a mistake (...), so no check may touch anything until it is fixed` | fixing the line named, then `holdfast trust` |
| `the policy file cannot be read, so no check may touch anything until it can` | fixing the file's permissions |

To find every blocked fact, and to make a script fail on them:

```console
$ holdfast check --quiet
7 facts: 1 broken, 3 blocked, 3 hold.

BROKEN    f-mfgekb  Staging answers
          proof failed: https://staging.example.com/health did not answer: the name does not exist · checked just now
          check: http 200 https://staging.example.com/health
BLOCKED   f-565frt  The NAS answers on 192.168.1.20
          not checked: the policy does not allow connections to 192.168.1.20. A person can allow it: holdfast allow net 192.168.1.20
          check: port 192.168.1.20:445
...
```

`holdfast check` exits 1 when a fact is broken or could not be checked in time. BLOCKED alone does not fail it; add `--strict` and it does.

If a blocked fact should not be checked at all, withdraw it: `holdfast retract <id> --reason "..."`.
