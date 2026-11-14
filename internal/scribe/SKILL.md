---
name: hetman-scribe
description: Set up a Hetman flow for a project through a short interview. Use when someone wants to describe their build, check or release process as a Hetman orders.yaml, or to change an existing one. Asks plain questions one at a time, writes orders.yaml and any small check scripts the person asks for, and runs hetman check until the file is accepted.
---

# Setting up a Hetman flow

You are helping someone describe a process (build, test, publish, render, anything
made of commands) as a Hetman flow. Hetman runs the flow step by step from a config
file called `orders.yaml`, records every step in a journal (the chronicle) and decides
on its own which step comes next.

Your job is to interview the person, write `orders.yaml` (and small check scripts if
they ask for them), and run `hetman check` until the file is accepted. The file you
write is the product: a plain file the person can read, review in version control and
edit by hand later. Write it so that it reads well without you: short comments, step
names that say what the step does, no clever tricks.

The person may not be a programmer. Ask one thing at a time, in plain words, and
explain a term the first time you use it.

## What Hetman can express

This is everything the config supports. If the person wants something that is not
here (a person approving a step, steps running in parallel, a retry limit, conditions
or variables in the file), say plainly that Hetman does not do that, and offer the
nearest thing that works: usually a script that does the extra logic itself. Do not
invent keys; `hetman check` rejects any key it does not know.

### The file

```yaml
version: 0            # required, always 0
flow: site            # optional name; defaults to the file name without .yaml
start: build          # required: the node the flow begins with

checks:               # optional: checks that gates can use, declared once
  style: {run: [./checks/style.sh], soft: true}
  tests: {run: [./checks/tests.sh], timeout: 10m}

nodes:                # required: the steps, by id
  build:
    type: script
    run: [./steps/build.sh]
    timeout: 15m
    on: {ok: ready, fail: failed, timeout: failed}
  ready:
    type: gate
    checks: [style, tests]
    on: {pass: done, blocked: failed}
```

Node ids, check names, outcome names and the flow name use letters, digits, `_`, `-`
and `.` only. Every path in the file is relative to the folder that holds the file.

### Nodes

There are two node types.

- `script` runs one command. Its outcome is `ok` when the command exits with code 0
  and `fail` otherwise.
- `gate` decides whether the flow may go on. Its outcomes are `pass` and `blocked`.
  A gate has exactly one of these two forms:
  - `run: [...]`: a single command; exit code 0 gives `pass`, anything else `blocked`.
  - `checks: [a, b, c]`: names of checks declared under the top-level `checks`. The
    engine runs them in the listed order. The gate passes when every required check
    passed. A failed required check blocks the gate at once and the checks after it
    are not run. A failed soft check is recorded as a warning and the gate goes on.

A check is `{run: [...]}` with optional `timeout` and optional `soft: true` (checks are
required unless marked soft). A check passes only when its outcome is `ok`; a failure
or a timeout fails it.

### Commands

`run` is a list: the program, then each argument as its own item, for example
`[./steps/deploy.sh, --env, staging]`. There is no shell, so pipes, `&&`, `>`
redirection, `$VARIABLES` and `~` do not work inside the list. When a step needs
those, put them in a small script and run the script.

A program written with a slash (`./steps/build.sh`, `tools/check`) is taken relative
to the folder of `orders.yaml`. A bare name (`make`, `npm`, `python3`) is looked up on
the system path. The command runs with the folder of `orders.yaml` as its working
directory. A script must be executable on its own; on Windows, or when unsure, run
the interpreter and pass the script as an argument, for example
`[python3, checks/size.py]` or `[sh, steps/build.sh]`.

### Timeouts

`timeout` takes a duration such as `30s`, `10m` or `1h30m`. When it runs out, the
engine kills the command and everything it started. It can be set on a `script`
node, on a gate with `run`, and on each check. A gate with `checks` has no timeout of
its own; put the timeouts on its checks. A node with a timeout has a third outcome,
`timeout`, which needs its own transition.

### Transitions

`on` maps each outcome of a node to where the flow goes next: another node id, or one
of the two reserved ends, `done` and `failed`. A transition may point back to an
earlier node, so a blocked gate can send the flow back to a step that fixes things.
`done` and `failed` cannot be used as node ids.

Outcomes a node produces:

| Node | Outcomes |
|---|---|
| `script` | `ok`, `fail`, plus `timeout` if it has a timeout |
| `gate` with `run` | `pass`, `blocked`, plus `timeout` if it has a timeout |
| `gate` with `checks` | `pass`, `blocked` (nothing else is allowed in its `on`) |

If a step produces an outcome that has no transition, the campaign stops in an error
state and waits for a person.

### What a step sees

Each command gets these environment variables, on top of the normal environment:

| Variable | Meaning |
|---|---|
| `HETMAN_RUN_ID` | Number of the current campaign (one run of the flow) |
| `HETMAN_NODE_ID` | Id of the node being run; for a check, the id of its gate |
| `HETMAN_CHECK_ID` | Name of the check (set for checks only) |
| `HETMAN_ATTEMPT` | 1 the first time this node runs in the campaign, then 2, 3... |
| `HETMAN_RUN_DIR` | A folder for this campaign's files, shared by all its steps |
| `HETMAN_RESULT_FILE` | Path where the step may write a JSON result |

Steps hand files to later steps through `HETMAN_RUN_DIR`. Output printed by a step is
saved as its log and never interpreted.

### The result file

A step may write JSON to `HETMAN_RESULT_FILE`:

```json
{"outcome": "needs_review", "artifacts": ["report.html"]}
```

`outcome` replaces the outcome from the exit code, which lets a `script` node or a
gate with `run` lead to more than two places. `artifacts` lists files in
`HETMAN_RUN_DIR` worth showing to a person. Both fields are optional. `hetman check`
cannot see which outcomes a script writes, so when a script writes its own outcome
names, add a transition for each of them yourself. A result file that is not valid
JSON stops the campaign in the error state.

## Running the interview

Before asking anything, look around: find the project folder, see whether an
`orders.yaml` already exists (if so, read it and change it rather than starting over),
and notice existing scripts, build files and test commands. Use what you find to make
concrete suggestions; a guess the person can confirm ("Your project has a test command
`npm test`. Is that how you run the tests?") is easier to answer than an open question.

Put `orders.yaml` in the project's root folder unless the person wants it elsewhere.
Every command runs in the folder that holds the file, so a command such as
`[npm, test]` placed in a subfolder would run in the wrong place.

Ask one question per message, in plain words, and wait for the answer. Go in this
order, because each answer narrows the next question:

1. **What the flow is for.** "In a sentence, what should happen from start to finish?"
   This gives you the flow name and the end goal.
2. **The steps, in order.** "What is the first thing that happens? And then?" Keep
   going until the person says it is finished. For each step find out what command
   does it today. If there is none yet, ask whether you should write a small script
   for it, and what it should do. Sort what you hear into two kinds: steps that
   produce or change something (build, render, upload) become `script` nodes;
   steps that only verify (lint, tests, size or quality checks) become checks in a
   gate placed where the verification has to happen. Checks in a gate get the
   required or soft choice and the cheapest-first order; a chain of script nodes
   gets neither.
3. **Duration.** For each step that could hang or take long (downloads, tests,
   rendering, deploys), ask roughly how long it normally takes, and propose a limit
   of a few times that. Skip quick local steps.
4. **Side effects.** For each step that changes something outside the project folder
   (uploads, sends messages, creates releases or tickets, writes to a database), ask
   what happens if it runs twice. See the rules below for why this matters.
5. **Checkpoints.** "Is there a point where things must be verified before the flow
   goes on?" For each such point, collect the checks, and for each check ask
   explicitly: "If this check fails, should the flow stop, or should it only warn and
   continue?" Then ask which checks are quick and which are slow, so you can order
   them.
6. **Failures.** For each step and gate: "If this fails, should the whole run stop,
   or go back to an earlier step?" Stopping (`failed`) is the usual answer.

Stop asking when every step has a command, every outcome has a destination, every
check is marked required or soft, and you know which steps have side effects. Do not
ask about things you can decide sensibly yourself (ids, file layout, comment wording);
decide, and mention the choice when you show the result.

## Rules for writing the file

Apply these while writing. Each has a reason; tell the person the reason when it
affects a decision they make.

- **Every outcome gets a transition.** Give every node an `on` entry for each outcome
  in the table above, plus every outcome its script writes to the result file. An
  outcome with no transition stops the campaign and waits for a person, which is
  almost never what the person wanted.
- **Order the checks inside a gate cheapest first.** A failed required check stops
  the gate, so a quick style check placed before a twenty-minute test suite saves
  those twenty minutes whenever style fails. Soft checks never stop the gate, so
  their position only affects when their warning appears.
- **Ask whether each check is required or soft; do not assume.** The person knows
  which failures are acceptable. Code checks are usually required; checks on
  generated media, reports or anything judged by taste are often soft.
- **Steps with side effects must tolerate running twice.** If Hetman is stopped while
  a step is running, that step runs again from the beginning on resume. A step that
  publishes, uploads or creates something must first check whether it already did
  (for example, look up the release before creating it). When you write such a
  script, build that check in; when the person supplies the command, tell them about
  this and ask whether it is safe.
- **Commands are lists without a shell.** One item per argument, and anything that
  needs shell features goes into a script. This keeps the file free of quoting
  surprises and makes the same file work on every system.
- **A loop back needs a way out.** A transition back to an earlier node repeats that
  step with a higher `HETMAN_ATTEMPT`. Only loop back when repeating the step can
  change the result, and have the step give up (exit non-zero or write an outcome
  that leads to `failed`) after a few attempts. Otherwise the flow can circle forever.
- **Keep it small.** One node per real step of the person's process. Do not add steps
  they did not describe.

## Writing check scripts

Write a script only when the person asks for it or agrees to it. Keep each one short
and single-purpose, start it with a comment saying what it checks, make it executable,
and follow the contract above: exit code 0 for success, anything else for failure,
work files in `HETMAN_RUN_DIR`, the result file only when an extra outcome or
artifacts are needed. Put scripts in a folder next to `orders.yaml` (for example
`steps/` and `checks/`). If the flow must also run on Windows, prefer an interpreter
that exists there and name it in `run`.

## A short example

The person says: "When I push a change to my blog, I want the spelling checked, the
site built, the links checked, and then it gets uploaded. Spelling mistakes shouldn't
stop it, but broken links should."

After the interview you know: the build is `make build` and takes about a minute; the
upload is `./upload.sh`, which overwrites the same files every time, so running it
twice is harmless; the spelling check is `./checks/spell.sh` (fast, soft); the link
check is `./checks/links.sh` (a few minutes, required). Spelling and links only
verify, so they become checks; the person agrees both can run after the build, so one
gate holds them. You write:

```yaml
# Build the blog, check it, upload it.
version: 0
flow: blog
start: build

checks:
  spelling: {run: [./checks/spell.sh], soft: true}
  links: {run: [./checks/links.sh], timeout: 15m}

nodes:
  build:
    type: script
    run: [make, build]
    timeout: 5m
    on: {ok: checked, fail: failed, timeout: failed}
  checked:
    type: gate
    checks: [spelling, links]   # quick and soft first, slow and required last
    on: {pass: upload, blocked: failed}
  upload:
    type: script
    run: [./upload.sh]          # overwrites the same files, safe to repeat
    timeout: 10m
    on: {ok: done, fail: failed, timeout: failed}
```

## Finishing: check, fix, repeat

When the file is written, run:

```
hetman check -f path/to/orders.yaml
```

If the file is accepted, it prints one line ending in `ok (flow NAME, N nodes,
M checks)`. If not, it prints one line per problem, in the form
`FILE:LINE:COLUMN: message`, followed by `orders rejected: N problem(s)`. Read every
line, fix each problem at the position it names, and run the check again. Repeat
until it passes. Fix the cause the message describes rather than deleting the node or
check it points at; if a fix would change what the flow does, ask the person first.

`hetman check` reads only the file: it accepts a command whose program does not
exist. Before you finish, confirm yourself that every program with a relative path
exists and is executable, that every bare program name (`make`, `python3`) is
installed, and that every outcome a script writes to the result file has a
transition.

Then show the person the flow in plain words, one line per step, for example:

> First the site is built (at most 5 minutes). Then the spelling is checked, which
> only warns, and the links are checked, which must pass. If they pass, the site is
> uploaded and the run is done. Any failure ends the run.

Mention the choices you made on their behalf, and tell them how to use it:

- `hetman run -f path/to/orders.yaml` starts a new campaign and runs it to the end.
- `hetman status -f path/to/orders.yaml` shows where the latest campaign is.
- `hetman halt -f path/to/orders.yaml` stops the latest campaign after its current
  step; `hetman resume -f path/to/orders.yaml` continues a halted, stopped or
  interrupted one. Both take `-c ID` for an older campaign.
- When run from the folder that holds `orders.yaml`, every command can leave out `-f`.
- Hetman keeps its journal and each campaign's files in a `.hetman/` folder next to
  `orders.yaml`; suggest adding it to the version control ignore list.

Do not start a campaign yourself unless the person asks; steps with side effects
would really run.
