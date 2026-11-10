# Security

Holdfast runs checks on an agent's behalf: it reads files, opens connections and, when you allow it, runs commands. This page says what keeps that inside the limits you set, and where the protection ends.

## The model

The agent proposes; a person decides. An agent can ask for any check on any path, host or command. Whether the check may run is decided by `.holdfast/policy`, a file that only a person changes, and a check outside it is not run at all: its fact is shown as BLOCKED until a person allows it.

| What could go wrong | What prevents it |
| --- | --- |
| A check reads a file it should not | Checks read only inside the folders on the policy's `files` lines; by default that is the project folder. Each file is opened through `os.Root`, which refuses a path or a symbolic link that leads out of the folder. |
| A check probes your network | `net` lines say who may be reached; by default only this machine. The decision is made at the moment of connecting, on the address actually being dialled, so a public name that points at a private address is treated as private. `localhost` is answered locally and never sent to a resolver. Redirects are judged again, at most five are followed, requests are GET only, and at most 1 MiB of a body is read. |
| A check runs a program | No command runs until a person adds a `cmd` line for it. The command is run directly, never through a shell, in its own process group, with an environment reduced to `PATH`, `HOME`, `LANG` and `LC_ALL`, and it is stopped after ten seconds. |
| The agent widens its own limits | The agent has four tools: `remember`, `recall`, `verify` and `retract`. None of them changes the policy, approves it, or vouches for a note. The browser ledger cannot change or approve the policy either. |
| A repository you clone brings a wide policy | A policy that grants more than the defaults does nothing until you approve it on your machine with `holdfast trust`, which shows it to you first. The approval is the SHA-256 of the file together with the folder it is in, kept in your own configuration folder, so a repository cannot carry one, and any change to the file needs approving again. |
| A facts file written by someone else misbehaves | Every line is parsed again with the same grammar and the same limits as a fact you type. Control characters, and the marks that change the order text is drawn in, are removed from every piece of text on reading, so a fact cannot rewrite your terminal or read differently from what it says, and a policy line that contains one is refused. |
| A fact's wording talks you into running a command | The command that allows a blocked check is the policy's own line, kept in a field of its own. The ledger offers only that field to copy and never looks for a command inside the evidence text, which quotes the check an agent wrote. In a terminal, the suggestion Holdfast makes is the last thing on the evidence line. |
| A pattern that never finishes | `matches` uses RE2, which cannot backtrack. A pattern longer than 512 bytes or too large once compiled is refused, files over 8 MiB are not scanned, and every check has a time limit. |
| A check that did not finish passes as proved | It is shown as TIMED OUT, with when it last held. It is never reported as HOLDS: as JSON its status is `timed_out`, an export files it under its own heading, and `holdfast check` exits 1. |
| Evidence leaks what a check saw | Evidence records where proof was found (a line number, a status code, a shortened hash), never the content of a file, a response body, its headers or a query string. |
| Another website talks to the ledger | `holdfast serve` listens on loopback and refuses any other address without `--expose`. Every call to the API must carry a header that a page from another site cannot send, and its Origin, when a browser sends one, must be the address the request was made to. Without `--expose`, a request is answered only if it is addressed to a name for this machine (`localhost` or a loopback address), which is what stops another site's name from being pointed at the ledger. The page is served with a Content-Security-Policy that forbids inline script and framing. |

## What this does not protect

- **An agent that has a shell.** The policy governs the memory's own tools. An agent that can also run commands can run `holdfast allow`, edit the policy file, or approve it; Holdfast cannot tell a person at a keyboard from a program that types. What the policy guarantees is narrower: the memory is never the way around your agent's own permission system, and a fact cannot carry a command that runs without a person having allowed it.
- **Approving without reading.** `holdfast trust` protects you from a policy someone else wrote. It does not protect you from approving one you did not read.
- **One question at a time about readable files.** `contains` and `matches` answer whether a text is in a file and on which line. An agent can ask that about any file inside the allowed folders, as often as it likes. The project folder is allowed by default, and that includes an `.env` file inside it, so keep secrets out of the folders checks may read.
- **Names sent to your resolver.** Once the policy allows `private` or `public` addresses, any host name in a check is looked up, including a name that is then refused because of its address.
- **What an allowed command does.** It runs with your rights. The policy matches the words of the command; it does not know what the program does with them.
- **`serve --expose`.** It puts the ledger on the network with no password. Anyone who can reach it can read the facts, add and retract them, though still not change the policy. Use it only on a network you trust.

## Reporting a problem

Please report a vulnerability privately to ali@gallowforge.dev rather than in a public issue. Expect a reply within a week.
