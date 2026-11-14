# Hetman

Hetman runs a project's flow (build, check, publish, or any other sequence of
commands) from a config file, one step at a time. The file names the steps, the
checks that must pass between them, and where the flow goes after each outcome.
Hetman decides every transition itself, records each step in a journal, and can
halt a run and resume it later, including after a crash.

The point is that the order of steps and the decision whether a check passed belong
to a program, not to whoever (or whatever) runs the steps. A step cannot skip a
gate, mark itself as passed or choose what comes next: it runs, exits, and Hetman
moves on according to the config.

## Install

The source is at <https://github.com/para1992/hetman>. Download the binary for your
system from its [Releases page](https://github.com/para1992/hetman/releases). Each
release has one file per target, named `hetman-VERSION-OS-ARCH` (`.exe` on Windows),
for Linux, macOS and Windows on amd64 and arm64, and a checksum file
`hetman-VERSION-SHA256SUMS`. Check the download, make it executable and put it on
your path:

```sh
sha256sum --check --ignore-missing hetman-v0.1.0-SHA256SUMS
chmod +x hetman-v0.1.0-linux-amd64
mv hetman-v0.1.0-linux-amd64 /usr/local/bin/hetman
```

On macOS use `shasum -a 256 --check --ignore-missing` instead of `sha256sum`.
`hetman version` prints the version of the binary.

To build from source you need Go (the version named in `go.mod`). Dependencies are
vendored, so no network access is needed:

```sh
go build -o hetman ./cmd/hetman
```

## Quick start

The repository contains a toy flow in `examples/toy`: four script steps and a gate
with two checks. The steps are POSIX shell scripts run through `sh`, which Linux and
macOS have. Windows has no `sh` of its own: run the example from a shell that puts
one on the path, such as Git Bash from Git for Windows. Your own flows do not need a
shell; see `run` under Concepts.

Check the config:

```sh
$ cd examples/toy
$ hetman check
/path/to/examples/toy/orders.yaml: ok (flow toy, 5 nodes, 2 checks)
```

Run it. Each run of a flow is a campaign:

```sh
$ hetman run
campaign 1 started (flow toy)
[1] prepare attempt 1 started
[1] prepare finished: ok (0s)
[1] prepare -> build
...
[1] ready attempt 1 started
[1]   check lint: ok
[1]   check tests: ok
[1] ready finished: pass (10ms)
[1] ready -> publish
[1] publish attempt 1 started
[1] publish finished: ok (6ms)
[1] publish -> done
[1] campaign done
```

See where it is, with every step and check:

```sh
$ hetman status
```

Make the required check fail and watch the gate block the flow (the soft check only
warns):

```sh
$ TOY_TESTS_FAIL=1 TOY_LINT_FAIL=1 hetman run
...
[2]   check lint: fail (soft, warning)
[2]   check tests: fail (required, gate blocked)
[2] ready finished: blocked (1ms)
[2] ready -> failed
[2] campaign failed
```

Halt a run and resume it. `TOY_SLEEP=20` makes the build step take 20 seconds; while
it runs, call `hetman halt` from a second terminal in the same folder:

```sh
$ TOY_SLEEP=20 hetman run          # terminal 1
$ hetman halt                      # terminal 2
campaign 3 will halt after its current step
```

The campaign stops before the next step (`halted before test`). Continue it with:

```sh
$ hetman resume
campaign 3 resumed at test
...
```

If you press Ctrl-C (or the process is killed) during a step, the campaign is
interrupted; `hetman resume` runs that step again from the beginning.

## Concepts

**Orders.** The config file, `orders.yaml` by default. It names the first node and
lists the nodes with their transitions. Every path in it is relative to the folder
that holds it.

```yaml
version: 0
flow: toy
start: prepare

checks:
  lint:  {run: [sh, steps/lint.sh], soft: true}
  tests: {run: [sh, steps/tests.sh], timeout: 1m}

nodes:
  prepare:
    type: script
    run: [sh, steps/prepare.sh]
    on: {ok: build, fail: failed}
  build:
    type: script
    run: [sh, steps/build.sh]
    timeout: 5m
    on: {ok: test, fail: failed, timeout: failed}
  test:
    type: script
    run: [sh, steps/test.sh]
    on: {ok: ready, fail: failed}
  ready:
    type: gate
    checks: [lint, tests]
    on: {pass: publish, blocked: failed}
  publish:
    type: script
    run: [sh, steps/publish.sh]
    on: {ok: done, fail: failed}
```

- `version` is required and is `0`. `flow` is an optional name (the file name by
  default). `start` is required.
- Ids and names use letters, digits, `_`, `-` and `.`.
- `run` is a list: the program followed by its arguments. There is no shell. A
  program with a slash in it is relative to the orders folder; a bare name such as
  `make` is looked up on the path. On Windows a program must be one Windows can
  start (`.exe`, `.cmd`, `.bat`); run a script through its interpreter, as in
  `[sh, steps/build.sh]` or `[python3, steps/build.py]`.
- `timeout` is a duration such as `30s`, `10m` or `1h30m`. When it runs out, the
  command and every process it started are killed.

**Nodes.** There are two types.

- `script` runs one command. Exit code 0 gives the outcome `ok`, anything else
  `fail`.
- `gate` decides whether the flow may go on, with the outcomes `pass` and
  `blocked`. It has either a single `run` command (exit code 0 gives `pass`) or a
  list of `checks`, never both.

**Transitions.** `on` maps each outcome of a node to the next node or to one of the
two ends, `done` and `failed`. A node with a `timeout` also has the outcome
`timeout`. Every outcome a node can produce must have a transition; `hetman check`
rejects the file otherwise. A transition may lead back to an earlier node.

**Checks.** Declared once under the top-level `checks` and listed by name in a gate.
The gate runs them in the listed order. A check is required unless it has
`soft: true`. A failed required check blocks the gate immediately and the checks
after it are not run, so list cheap checks first. A failed soft check is recorded
as a warning and does not block. A check passes only on the outcome `ok`; a timeout
fails it. A gate with checks has no timeout of its own; each check can have one.

**Campaign.** One run of a flow, numbered from 1. `hetman run` always starts a new
campaign.

**Chronicle.** The journal of every campaign: each step started and finished, each
check, each transition, halt and resume. Hetman replays it before every move, so a
restarted process continues exactly where the previous one stopped. The chronicle
and the files of each campaign live in `.hetman/` next to the orders file:

```
.hetman/
  chronicle.db
  campaigns/1/work/      HETMAN_RUN_DIR of campaign 1
  campaigns/1/logs/      output of each step and check, per attempt
  campaigns/1/results/   result files
```

Add `.hetman/` to your version control ignore list.

**Halt and resume.** `hetman halt` asks a campaign to stop after its current step;
if no process is running it, it halts at once. `hetman resume` continues a halted
campaign, a campaign interrupted in the middle of a step (that step runs again), or
a campaign stopped in the error state. A campaign stops in the error state, and
waits for a person, when a program cannot be started, a result file is not valid
JSON, an outcome has no transition, or the current node is no longer in the orders.
Fix the cause and resume. The orders file is read again on every resume.

## Commands

```
hetman check  [-f FILE]                 check the orders without running anything
hetman run    [-f FILE]                 start a new campaign and run it
hetman status [-f FILE] [-c ID] [-events]  show where a campaign is
hetman halt   [-f FILE] [-c ID]         halt a campaign after its current step
hetman resume [-f FILE] [-c ID]         continue a halted, stopped or interrupted campaign
hetman serve  [-f FILE] [-addr ADDRESS] show every campaign live on a web page (Watchtower)
hetman scribe [-dir DIR] [-force]       write the setup assistant skill for a coding agent
hetman version                          print the version of hetman
```

- `-f FILE` is the orders file, `orders.yaml` in the current folder by default.
- `-c ID` is the campaign number; the latest campaign by default.
- `-events` makes `status` also list every chronicle event.
- `-addr ADDRESS` is where `serve` listens, `127.0.0.1:8740` by default.
- `-dir DIR` is the folder `scribe` writes `SKILL.md` into,
  `.agents/skills/hetman-scribe` in the current folder by default. `-force` replaces
  a different `SKILL.md` that is already there.

`scribe` and `version` need no orders file.

`hetman check` prints `FILE: ok (...)` or one line per problem as
`FILE:LINE:COLUMN: message`. Besides the file itself, it looks for the program of
every `run` list. A program written with a path that does not exist, is not a file
or is not executable is a problem. A bare name that is not on the path of this
computer is printed as `FILE:LINE:COLUMN: warning: ...` and does not fail the check:
the flow may run on another computer, or an earlier step may install the program.
`hetman run` and `hetman resume` make the same check before they start a step:
problems stop them, warnings are printed and they go on. A bare program that still
cannot be started stops the campaign in the error state.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | done (or the command succeeded) |
| 1 | the campaign reached `failed`, or the command failed |
| 2 | bad arguments or rejected orders |
| 3 | halted |
| 4 | stopped in the error state |
| 130 | interrupted |

## Writing a step

Every command Hetman runs (a script node, a gate's `run`, a check) follows the same
contract.

It runs in the folder that holds the orders file, with the normal environment plus
these variables:

| Variable | Meaning |
|---|---|
| `HETMAN_RUN_ID` | Campaign number |
| `HETMAN_NODE_ID` | Node id; for a check, the id of its gate |
| `HETMAN_CHECK_ID` | Check name (checks only) |
| `HETMAN_ATTEMPT` | 1 the first time the node runs in this campaign, then 2, 3... |
| `HETMAN_RUN_DIR` | Folder for this campaign's files, shared by all its steps |
| `HETMAN_RESULT_FILE` | Path where the step may write a JSON result |

**Exit code.** 0 means success (`ok`, or `pass` for a gate), anything else failure
(`fail`, or `blocked`).

**Result file.** Optionally, the step writes JSON to `HETMAN_RESULT_FILE`:

```json
{"outcome": "nothing_new", "artifacts": ["report.html"]}
```

`outcome` replaces the outcome from the exit code, so a step can lead to more than
two places; give each such outcome a transition in `on`, because `hetman check`
cannot know about it. `artifacts` lists files in `HETMAN_RUN_DIR` worth looking at;
`hetman status` shows them. Both fields are optional. The result file is ignored
after a timeout.

**Output.** Standard output and standard error are saved as the step's log and are
never interpreted.

**Running twice.** If Hetman is stopped while a step is running, the step runs again
from the beginning on resume; a finished step is never repeated. A step with side
effects must therefore tolerate a second run: a step that creates a release should
first check whether the release already exists.

## Watchtower

`hetman serve` starts Watchtower, a web page that shows the campaigns of one orders
file live:

```sh
$ hetman serve
watchtower for /path/to/orders.yaml on http://127.0.0.1:8740/ (Ctrl+C to stop)
```

It listens on `127.0.0.1:8740` by default, so only this computer can reach it; use
`-addr` for another address. The page lists the campaigns, newest first. For each
campaign it shows the steps and checks with their attempts, outcomes, logs and
artifacts, a timeline, and the chronicle events. Everything on it comes from the
chronicle, as with `hetman status`, and updates while campaigns run.

It has two controls, Halt and Resume, which do what `hetman halt` and
`hetman resume` do. A campaign resumed from the page is run by the `hetman serve`
process; stopping that process interrupts it, and `hetman resume` continues it.

## Setup assistant

Scribe is the setup assistant: `SKILL.md`, an instruction file for a coding agent,
built into the binary. Given to an agent, it interviews a person about their project
in plain language, writes `orders.yaml` and any small check scripts they ask for, and
runs `hetman check` until the file is accepted. The result is an ordinary orders file
that can be read and edited by hand.

To hand it to an agent, run this in the project folder:

```sh
$ hetman scribe
wrote .agents/skills/hetman-scribe/SKILL.md
```

Agents that read skills from `.agents/skills` find it there. For an agent that looks
in another folder, pass that folder with `-dir`, or point the agent at the file. Then
ask the agent to set up a Hetman flow for the project.

Running `hetman scribe` again prints `PATH is up to date` when the file is the same.
When it differs (an older version, or your own edits), it is left alone and the
command fails; `hetman scribe -force` replaces it.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
