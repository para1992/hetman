# Hetman

Hetman runs a project's flow (build, check, publish, or any other sequence of
commands and coding-agent steps) from a config file, one step at a time. The file names the steps, the
checks that must pass between them, and where the flow goes after each outcome.
Hetman decides every transition itself, records each step in a journal, and can
halt a run and resume it later, including after a crash.

The point is that the order of steps and the decision whether a check passed belong
to a program, not to whoever (or whatever) runs the steps. A step cannot skip a
gate, mark itself as passed or choose what comes next: it runs, exits, and Hetman
moves on according to the config. That holds for steps done by a coding agent too:
the agent answers one step in a checked format, and the config alone says where the
flow goes next. People take part the same way: a gate can put questions to a person,
a human node waits for a person's decision, and an agent step can interview a person
before it answers, all at the points the config names.

## Install

The source is at <https://github.com/para1992/hetman>. Download the binary for your
system from its [Releases page](https://github.com/para1992/hetman/releases). Each
release has one file per target, named `hetman-VERSION-OS-ARCH` (`.exe` on Windows),
for Linux, macOS and Windows on amd64 and arm64, the same for each adapter (see
Adapters), and a checksum file `hetman-VERSION-SHA256SUMS` for all of them. Check the
download, make it executable and put it on your path:

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

### Agent steps without a real client

`examples/agent` is a flow of two agent steps, a draft and a review that can send
the draft back. Its steps run through a stub adapter that runs no model and follows
a plan file, so it works anywhere. Build the stub once, then run the flow with its
input:

```sh
$ go build -o examples/agent/bin/stub-adapter ./cmd/hetman-stub-adapter
$ cd examples/agent
$ hetman run -input input.json
campaign 1 started (flow agent-demo)
[1] draft attempt 1 started (executor stub)
[1]   draft session draft-session-1
[1] draft finished: ok (2.005s)
[1] draft -> review
[1] review attempt 1 started (executor stub)
[1] review finished: needs_rework (1.003s)
[1] review -> draft
[1] draft attempt 2 started (executor stub)
...
[1] review finished: approved (1.007s)
[1] review -> done
[1] campaign done
```

`STUB_PLAN=plans/NAME.json` picks another plan: `invalid` (an answer that does not
match its schema, then a retry that is told what was wrong), `exhausted` (every
attempt fails), `limit` (a subscription limit; the campaign waits 20 seconds and
goes on), `offline` (lost connectivity; `hetman resume` runs the step again) and
`slow` (a one-minute step to stop and resume). Set the variable for `hetman resume`
too, since the stub reads it on every step.

### People in the flow

`examples/person` puts people into a flow: an agent step that asks a person questions
before it answers (two rounds), a plan written by an agent, a gate where a person
approves the plan or sends it back, and a human node that decides how to go on. It
uses the same stub adapter, plus `sh` for its check and its notification hook:

```sh
$ go build -o examples/person/bin/stub-adapter ./cmd/hetman-stub-adapter
$ cd examples/person
$ hetman run -input input.json
campaign 1 started (flow person-demo)
[1] interview attempt 1 started (executor stub)
[1]   interview session interview-1
[1] interview asks the person 2 question(s), round 1 of at most 2 (1.007s)
[1] interview waits for a person (2 question(s)); answer with: hetman answer -f /path/to/examples/person/orders.yaml -c 1
```

The campaign waits. From a second terminal in the same folder, see what is asked and
answer it, in the terminal or with flags:

```sh
$ hetman status
...
agent step (round 1) interview asks (question #6, 2026-11-14 22:30:55):
  1. May the wiki be read-only for an hour during the move?  [downtime: yes/no]
  2. When should the move happen?  [when: this week/next month]
  answer: hetman answer -f /path/to/examples/person/orders.yaml -c 1
      or: hetman answer -f /path/to/examples/person/orders.yaml -c 1 -ask 6 -a downtime=yes -a 'when=this week'
$ hetman answer
...
$ hetman answer -a keep="the start page"
```

The waiting `hetman run` picks each answer up and goes on. Answering `no` at
`plan_approved` sends the plan back, with the answers and comments in its prompt.
`hetman serve` shows the same campaign and has an answer form on the card of the
waiting step.

### Work items in parallel

`examples/foreach` has an agent step write a plan, a list of work items where some
need others first, and a `foreach` node run a small segment (build, then test) once
per item, two at a time. The test step holds the lock `test-stand`, so no two tests
ever run at once:

```sh
$ go build -o examples/foreach/bin/stub-adapter ./cmd/hetman-stub-adapter
$ cd examples/foreach
$ hetman run -input input.json
...
[1] build-all: item storage runs as campaign 2
[1] build-all: item logo runs as campaign 3
[2 build-all/storage] test waits for the lock test-stand, held by campaign 3 (build-all/logo) of campaign 1 at test, attempt 1, process 4242
...
[1] build-all finished: done (5 of 5 item(s) done, 8.205s)
$ hetman status
...
  build-all       foreach      finished  done     1         8.205s
    item storage  campaign 2   finished  done
    item api      campaign 4   finished  done
...
```

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
  default). `start` is required. `language` optionally names the language of what
  the flow addresses to a person (see Language for the person). `scribe` optionally
  names the executor and model the setup assistant may ask (see Setup assistant);
  the engine ignores it. `workdir` names the folder agent steps work in (see Agent
  steps); orders with agent nodes must name it here or on each agent node.
- Ids and names use letters, digits, `_`, `-` and `.`.
- `run` is a list: the program followed by its arguments. There is no shell. A
  program with a slash in it is relative to the orders folder; a bare name such as
  `make` is looked up on the path. On Windows a program must be one Windows can
  start (`.exe`, `.cmd`, `.bat`); run a script through its interpreter, as in
  `[sh, steps/build.sh]` or `[python3, steps/build.py]`.
- `timeout` is a duration such as `30s`, `10m` or `1h30m`. When it runs out, the
  command and every process it started are killed.

**Nodes.** There are six types.

- `script` runs one command. Exit code 0 gives the outcome `ok`, anything else
  `fail`.
- `gate` decides whether the flow may go on, with the outcomes `pass` and
  `blocked`. It has either a single `run` command (exit code 0 gives `pass`) or a
  list of `checks`, never both, and it may ask a person questions (see Questions to
  a person).
- `human` waits for a person, who answers its questions and chooses its outcome.
- `agent` gives one step to a coding agent through an executor adapter. Its outcomes
  are the values its output schema allows for `outcome`, plus `fail`. See Agent
  steps below.
- `segment` runs another orders file, a segment, to its end as a child campaign and
  takes the end it reached, `done` or `failed`, as its outcome.
- `foreach` runs a segment once for every item of a list an earlier step wrote,
  several at a time, and ends `done` when every item did. See Segments, for-each and
  locks.

Any node may also name `locks`: resources its steps hold exclusively.

**Transitions.** `on` maps each outcome of a node to the next node or to one of the
two ends, `done` and `failed`. A node with a `timeout` also has the outcome
`timeout`. Every outcome a node can produce must have a transition; `hetman check`
rejects the file otherwise. A transition may lead back to an earlier node.

A transition can be limited: written as `{to: TARGET, max: N, then: OUTCOME}`, it is
taken at most N times (1 to 100) in one campaign; when the node produces its outcome
once more, the flow takes the transition of OUTCOME instead. OUTCOME is a name of your
choosing with a transition of its own under the same `on`, even on a gate, whose own
outcomes are only `pass` and `blocked`; it may be limited in turn, but limits may not
name each other in a circle.

```yaml
  tests:
    type: gate
    checks: [unit]
    on: {pass: done, blocked: {to: fix, max: 3, then: stuck}, stuck: ask_lead}
```

The count is read from the chronicle, so it survives a restart, and each child
campaign of a segment or for-each counts its own. A transition taken instead counts
for the outcome it was taken on, and is recorded with the outcome the node produced
(`instead`). `hetman retry` keeps the count: it runs a failed step again and undoes no
transition. `hetman rewind -to NODE` undoes the transitions taken since the campaign
first reached NODE (in the history earlier rewinds kept), so those no longer count; a
rewind to a node after the loop leaves the count as it is.

**Checks.** Declared once under the top-level `checks` and listed by name in a gate.
The gate runs them in the listed order. A check is required unless it has
`soft: true`. A failed required check blocks the gate immediately and the checks
after it are not run, so list cheap checks first. A failed soft check is recorded
as a warning and does not block. A check passes only on the outcome `ok`; a timeout
fails it. A gate with checks has no timeout of its own; each check can have one.

**Campaign.** One run of a flow, numbered from 1. `hetman run` always starts a new
campaign. `hetman run -input FILE` gives it a run input: a JSON object that prompt
templates read. It is stored in the chronicle with the campaign, so a resumed
campaign sees the same input. Every command Hetman starts for the campaign (script
nodes, gates, checks, the notification hook, adapters) finds the same input in
`input.json` in `HETMAN_RUN_DIR`, named by `HETMAN_INPUT_FILE`; a campaign without
input gets `{}`. Hetman writes the file from the chronicle and rewrites it before
every command whenever it is missing or differs, so a script cannot change the input
for the steps after it. One generic check can thus serve every task: it reads the
task from the input and acts on it. `examples/acceptance` shows this with an
acceptance script written per task, proven valid against a good and a bad fixture,
frozen by a fingerprint and run by one generic check.

**Chronicle.** The journal of every campaign: each step started and finished, each
check, each transition, halt and resume. Hetman replays it before every move, so a
restarted process continues exactly where the previous one stopped. The chronicle
and the files of each campaign live in `.hetman/` next to the orders file:

```
.hetman/
  chronicle.db
  campaigns/1/work/      HETMAN_RUN_DIR of campaign 1
  campaigns/1/logs/      output of each step and check, per attempt
  campaigns/1/results/   result files, and the answers of agent steps
  campaigns/1/prompts/   the rendered prompt of every agent attempt
  campaigns/1/work/answers/NODE.json   the latest answers given at a gate or human node
  campaigns/1/work/input.json          the run input (HETMAN_INPUT_FILE)
  campaigns/1/work/outputs/NODE.json   the output of the latest accepted attempt of agent node NODE
```

`results/` keeps the output of every agent attempt, including rejected ones; a script
that needs what an agent node said reads `outputs/NODE.json` in `HETMAN_RUN_DIR`
instead. It holds the validated output object and is written, like `input.json`,
from the chronicle before every step, so it is restored after a crash or an edit;
it does not exist until the node has an accepted output.

Add `.hetman/` to your version control ignore list. Agent steps may not write it,
except the campaign's own `work/` folder (see Working folder below).

**Halt and resume.** `hetman halt` asks a campaign to stop after its current step;
if no process is running it, it halts at once. `hetman resume` continues a halted
campaign, a campaign interrupted in the middle of a step (that step runs again), or
a campaign stopped in the error state. A campaign stops in the error state, and
waits for a person, when a program cannot be started, a result file is not valid
JSON, an outcome has no transition, the current node is no longer in the orders, an
adapter does not describe itself properly, or a prompt template reads a run input
field that does not exist. Fix the cause and resume.

**Changing the orders while a campaign runs.** Before every step a campaign looks at
the files of its orders (the orders file, its prompt templates, schemas and programs,
and every segment's) and reads the orders again when any of them changed, so an edit
takes effect at the next step that starts. This holds alike for the campaign a person
started and for its running children: a child takes its segment from the orders read
afresh, through the nodes that led to it, so an operation on a segment's file (say
`hetman scribe set build effort high -f task.yaml`) reaches the next step of every
child running that segment. The progress output says `FILE changed; the next steps use
it as it is now`. Orders that do not pass the check at that moment are not used: the
campaign goes on with the version it read before and says why. A step already running
finishes with the version it started with, and a for-each keeps the list it recorded
when it started (see Segments, for-each and locks).

**Retry and rewind.** `hetman retry` runs the failed step of a campaign again with a
fresh attempt budget and continues from there. It applies to a campaign that ended in
`failed` (the step whose outcome led there runs again), one stopped in the error
state (the step it stopped at), and one whose current step failed its last allowed
attempt before its transition was recorded. `hetman rewind -to NODE` sends a campaign
back to a node it has already been at, whether it ended or not; that node and every
step after it run again. Each is one chronicle event (`retried`, `rewound`) with who
asked and from where; nothing recorded before it changes, so every earlier attempt,
answer and outcome stays visible. Replaying the events makes the node the position
again as if the flow had just moved there: the end, an error stop, a halt, a wait and
an open question are left behind, and the attempt budget of the visit starts afresh.
Attempt numbers keep counting, so the logs, results and prompts of earlier attempts
are never written over. Nothing in the work folder is deleted or restored: the steps
that run again find the files as the last step left them, and `outputs/NODE.json`
keeps the last accepted output of a node until it runs again. An agent step that runs
again because of a retry or rewind gets a `{{rework}}` saying a person sent it back.
Both are refused, and record nothing, while a process drives the campaign (halt it
first), and for a node the campaign never visited or that is no longer in the orders.
The command that asks then drives the campaign, as `resume` does.

With segments and for-each, retry and rewind act on the campaign a person started;
asked of a child campaign they are refused and name that campaign. They are also
refused while any unfinished child is driven. Sending a campaign back to a segment or
for-each node, or to a node before it, runs that node again as a new attempt with new
children; the children of earlier attempts keep their history, stay listed (marked
with their attempt), and are never driven again. A retry of a for-each node runs the
list of the attempt before again, keeps the items that reached `done` there
(`item_kept`) and runs the others as new children. A retry of a segment node whose
child failed or stopped in the error state retries that child at its own failed node.
A campaign stopped in the error state because a child did is retried inside the
deepest child that stopped by itself; the campaign keeps its running step and
continues it.

## Agent steps

An agent node hands one step to a coding agent. The agent gets a prompt, works, and
answers with a JSON object; Hetman checks the answer against a schema and follows
the transition of the outcome it names.

```yaml
executors:
  coder: {run: [./adapters/my-adapter, --some-flag]}

nodes:
  implement:
    type: agent
    executor: coder                  # a name under executors
    prompt: prompts/implement.md     # prompt template
    schema: schemas/implement.json   # schema of the answer
    workdir: ..                      # the folder the agent works in, relative to this file
    model: "..."                     # optional, passed to the adapter unchanged
    effort: "..."                    # optional, passed to the adapter unchanged
    tools: [read, edit]              # optional, passed to the adapter unchanged
    timeout: 30m                     # optional: limit for one attempt
    idle_timeout: 5m                 # optional: limit for silence from the adapter
    max_attempts: 3                  # optional, 1 to 100, default 3
    fail_on_denied: true             # optional: an answer after a denied tool call fails the attempt
    on: {ok: review, fail: failed}
```

**Executors.** `executors` maps a name to the program of an adapter, an argv list
like `run` of a script. The adapter is what talks to a client; Hetman never knows
which one. `hetman check` looks for the adapter program as it does for scripts, and
asks an adapter that declares the `check` feature to check the `model`, `effort` and
`tools` of its agent nodes without contacting a model; what the adapter rejects is
reported at its line of the orders file.

**Tools.** `tools` is passed to the adapter as written; which names and patterns a
client honours is for its adapter to say (see Adapters). `hetman check` rejects an
empty entry and one whose parentheses do not pair up, as `Bash(make` is when a list
was split at a space inside an entry. An adapter rejects an entry it knows its client
would accept and never honour as a `permanent` error naming the entry, before any
model request.

**Working folder.** `workdir` names the folder the agent works in, relative to the
orders folder: `.` for the orders folder itself, `..` for a project whose flow lives
in a subfolder such as `flow/orders.yaml`. It is set on the node or once at the top
level for every agent node that names none; there is no default, so orders with agent
nodes must set it somewhere. It may not lie in `.hetman/` (except inside a campaign's
own work folder) and must exist when the step runs, else the campaign stops in the
error state. A segment's orders name their own. The adapter runs the client there and
keeps the client's file tools inside it.

Whatever the folder, Hetman's own files are out of an agent's reach: each attempt
tells the adapter to protect `.hetman/` (the chronicle and every campaign folder) and
the files of the orders (the orders file, its prompt templates, schemas and the
programs it names with a path, and the same for every segment), and to keep the
campaign's work folder writable, since steps hand files to each other there; when the
work folder lies outside `workdir` it is opened to the file tools as well. An adapter
says it does this by declaring the `protect` feature; for one that does not, Hetman
says so once per run. A client enforces it only for its own file tools: a shell
command the agent is allowed to run can still write anywhere the user can, and
scripts, gates and checks, which are trusted code, are not restricted at all. See
each adapter for exactly what its client enforces.

**Schema.** The answer of an agent step is checked against a JSON file written in
this subset of JSON Schema: `type` (`object`, `array`, `string`, `number`,
`integer`, `boolean` or `null`), `properties`, `required`, `enum`, `items` and
`additionalProperties` (`true`, `false` or a schema). `title`, `description` and
`$schema` are allowed and ignored; any other keyword is rejected. The top level must
be an object with a required string property `outcome` whose `enum` lists the
outcomes the step may report. `hetman check` requires a transition for each of them
and for `fail`, and rejects a transition for an outcome the enum does not allow.

```json
{
  "type": "object",
  "properties": {
    "outcome": {"type": "string", "enum": ["approved", "needs_rework"]},
    "notes": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["outcome"],
  "additionalProperties": false
}
```

**Prompt templates.** Plain text with placeholders in double braces. There are no
expressions, conditions or loops.

| Placeholder | Becomes |
|---|---|
| `{{input}}` | the run input, as JSON |
| `{{input.a.b}}` | a field of the run input |
| `{{output.NODE}}` | the last accepted answer of agent node NODE, as JSON |
| `{{output.NODE.a}}` | a field of that answer |
| `{{artifact.NODE.FILE}}` | the text of FILE in the work folder, when script or gate NODE listed it in its result file, or, for a gate with checks, when one of its checks did in the gate's latest attempt (a check after a required one that failed did not run). All of them are files of the one work folder, so a name two checks list is one file, as the later check left it |
| `{{answers.NODE}}` | the latest answers a person gave at gate or human node NODE: each question, answer and comment, and the chosen outcome |
| `{{answers.NODE.ID}}` | the answer to question ID there (`yes`, `no`, an option or the text) |
| `{{answers.NODE.ID.comment}}` | the comment on that answer |
| `{{language}}` | the English name of the campaign's language for the person (`English` when none is set) |
| `{{language.native}}` | its own name, in its own script |
| `{{language.code}}` | its code, such as `hi` |
| `{{work}}` | the absolute path of the campaign's work folder (`HETMAN_RUN_DIR`), where steps hand files to each other |
| `{{rework}}` | empty on the first visit of the node; when the flow came back to it, the step that sent it back, its outcome and its answer, failed checks or artifacts, and what a person answered there |

A text value is inserted as it is, any other value as indented JSON. A node id may
contain dots; the longest node id that fits is used. A run input field that does not
exist stops the campaign in the error state; an answer or artifact that does not
exist yet gives empty text. `{{rework}}` looks through a script node that only passes
the flow on, one whose run in that visit listed no artifacts (such as a script that
counts the rounds of a loop and picks the way back): it names the step whose
transition led to that script, and the scripts it passed. Gates, agent, human,
segment and for-each nodes are never looked through. `hetman check` reports an unknown placeholder, an
`output` of a node that is not an agent, and a field the node's schema rules out,
at its line in the template. `{{` not followed by a name and `}}` stays as text. The
prompt of every attempt is stored under `prompts/` in the campaign folder.

**Language for the person.** The top-level key `language: CODE` makes everything
the flow addresses to a person come in that language: questions, summaries, plans and
other documents a person reads. Everything else stays English. For every attempt of
every agent step, Hetman adds this to the end of the rendered prompt (before its own
notes on asking questions and on a rejected answer), here for `language: hi`:

```
---
Write all text meant for a person in Hindi (हिन्दी): questions for the person, summaries, plans and any other document a person is meant to read. Code, code comments, identifiers, commit messages, file names and anything written into the repository stay in English unless this step's prompt says otherwise.
```

Without the key nothing is added and the prompts are exactly the templates. Every
command Hetman starts for the campaign (scripts, gates, checks, the notification hook,
adapters) gets the code in `HETMAN_LANGUAGE`, so it can write its own messages in that
language; without a language the variable is not set. The code is one of these six,
the five most spoken languages of the world by total number of speakers (Ethnologue
200, 2025) and Ukrainian (BCP 47 codes; `hetman check` rejects any other, at its line):

| Code | Language |
|---|---|
| `ar` | Arabic (العربية) |
| `en` | English (English) |
| `es` | Spanish (español) |
| `hi` | Hindi (हिन्दी) |
| `uk` | Ukrainian (українська) |
| `zh` | Mandarin Chinese (普通话) |

A person can choose another language for themselves without editing the shared file:
`HETMAN_LANGUAGE=CODE hetman run` overrides the key for that campaign (an empty
variable is no override, an unknown code is an error and starts nothing). The language
in effect is recorded in the chronicle when the campaign starts and stays the same on
`hetman resume`, whatever the file or the variable say by then. `hetman status` and
the campaign page show it. The language Hetman's own messages are written in is a
separate matter; see Interface language.

**Attempts.** Each node visit has `max_attempts` attempts. An attempt fails when the
answer does not match the schema, the adapter reports a transient error, the adapter
exits without a proper answer or breaks the protocol, the attempt runs past
`timeout`, or the adapter sends no message for `idle_timeout`; it is then run again.
After a rejected answer, the next prompt ends with what was wrong with it. When the
attempts are used up, the node's outcome is `fail`. A permanent error gives `fail`
at once. A node the flow comes back to starts with all its attempts again.

**Denied tool calls.** An adapter reports the tool calls its client denied in a step
with the answer; they are recorded with the attempt and shown in `hetman status`
(`denied` lines under the attempt), `hetman scribe explain` and the step card. By
default the answer is still accepted. With `fail_on_denied: true` on the node, an
answer after any denied call fails the attempt (failure `denied`, which uses an
attempt) unless the answer is `cannot_proceed`, and the next attempt's prompt quotes
the denied calls and asks the agent to work with the tools it has or to say it cannot
proceed. Set it on a step that must run tests or other commands, so that an agent
whose only test command was denied cannot answer `ok` without having run it.

**Cannot proceed.** An agent that cannot do its step at all, for example because a
tool or file it needs is denied, answers with the reserved outcome `cannot_proceed`
and its reason in `cannot_proceed_reason`. Hetman adds both to the schema the adapter
gets and tells the model about them at the end of every first prompt of an attempt:

```
---
If you cannot do this step at all, for example because a tool or file you need is denied or out of reach, or the task contradicts itself, do not guess and do not pretend it is done: reply with a JSON object whose "outcome" is "cannot_proceed" and whose "cannot_proceed_reason" says in a sentence or two what stops you, naming the tool or file. Fill any other required field with an empty value. A person reads the reason; this step is not retried by itself.
```

Such an answer is not retried. With a transition for `cannot_proceed` under `on`, the
flow follows it like any other outcome (the answer, with its reason, is the node's
output and the next step's `{{rework}}`); without one, the attempt fails with the
failure `unable` and the campaign stops in the error state with the reason, waiting
for a person, who fixes the cause and runs `hetman retry`. An answer with that outcome
and no reason is invalid output. The schema of a node may not list `cannot_proceed`
nor have a property `cannot_proceed_reason`.

**Limits and connectivity.** When the adapter reports a subscription limit, the
attempt does not count. If the adapter says when to retry, the campaign waits until
then and goes on by itself; the wait is in the chronicle and holds across a restart
(`hetman resume` continues waiting). Without a time, and when the adapter reports
lost connectivity, the attempt is lost, the campaign stops and waits for a person
(exit code 5), and `hetman resume` runs the step again.

**Sessions.** An adapter may report a session id for the conversation of a step.
When an attempt was cut off (Hetman stopped, a limit, lost connectivity) and the
adapter declares the `resume` feature, the next attempt is given that id so the
adapter can continue the conversation; otherwise the step starts fresh.

### Roles: several clients for one step

A role is a pool of clients a step may run on. Each client is an executor with its
own model and effort, since a model name means nothing to another client. An agent
node names a role instead of `executor`, `model` and `effort`; naming both is an
error, and a node that names an executor works as before.

```yaml
executors:
  first: {run: [./adapters/first-adapter]}
  second: {run: [./adapters/second-adapter]}

roles:
  coder:
    select: quota                  # required: fixed, round_robin or quota
    clients:                       # required: at least one
      - {executor: first, model: "...", effort: "...", allowance: {five_hour: 40, weekly: 300}}
      - {executor: second, model: "...", allowance: {five_hour: 2h, monthly: 80h}}

nodes:
  implement:
    type: agent
    role: coder                    # instead of executor, model and effort
    prompt: prompts/implement.md
    schema: schemas/implement.json
    on: {ok: review, fail: failed}
```

There are no defaults: a role says how it selects and lists its clients.

| `select` | The client of an attempt |
|---|---|
| `fixed` | the first client of the list that can run |
| `round_robin` | the next client after the one chosen last for this role, in the campaign and every child campaign of its segments and for-each steps; the turn is read from the chronicle, so it survives a restart |
| `quota` | the client that has used the smallest share of its allowance (below) |

**Which client can run.** In every mode, a client whose last attempt, in any
campaign of the folder, hit a subscription limit that resets at a known time is not
chosen until then; one whose attempt in this visit of the step hit a limit with no
reset time is not chosen again until a person resumes the campaign. In the `quota`
mode a client that has used up either window is not chosen until that window resets.
Before the mode decides, the client that ran the step's cut-off attempt (Hetman
stopped, a limit that has reset, lost connectivity) or asked the person questions
continues, if it can run, so that its conversation goes on.

**Ordering by remaining subscription.** Each client has a share of two windows, the
last five hours and the week: 0 is nothing used, 1 the whole allowance. A window's
share is *measured* when the adapter reports it (the `usage` feature, below);
otherwise *estimated* from what Hetman saw, when the client states an allowance for
that window; otherwise unknown. From its known shares each client gets two numbers:
the larger share (the window that will stop it first) and the smaller one; with one
known window both are that share. The client with the smallest larger share runs; a
tie goes to the smaller second share, then to the client listed first. A client with
no known share in either window comes after every client with one. A client with a
share of 1 or more in a window is skipped until that window's reset, and a limit the
client reported makes the window its reset falls in (five hours when it is at most
five hours after the limit, else the week) used up until then.

**Estimates.** `allowance` on a client gives, per window, either a number of attempts
(`five_hour: 40`) or a duration of work (`weekly: 20h`); `monthly:` instead of
`weekly:` is converted to a weekly allowance (times 12/52). Hetman counts the attempts
of that executor's agent steps that finished in the window in every campaign of the
same `.hetman` folder (attempts cut off by a limit or lost connectivity are left out)
and, for a duration, adds up how long they took; the share is that count or time
divided by the allowance. The windows roll: an estimate used up resets when enough
of its oldest attempts have left the window. An estimate is only what Hetman saw:
work done with the same subscription outside Hetman is not in it, and it is always
shown as estimated. An executor name stands for one client account across the orders
files of a folder.

**Changing client after a limit.** When the client of an attempt reports a
subscription limit, the attempt does not count and the step moves at once to the next
client that can run, chosen by the mode as above, without waiting. Only when no client
of the role can run does the campaign wait as for any limit: until the earliest time
one of them can run again, and then it goes on by itself; when none says when, it
waits for a person (exit code 5) and `hetman resume` tries the clients again. Lost
connectivity does not change client: it is the computer's connection, not the
client's allowance, so another client would most likely fail the same way, and the
step waits for a person as before.

**Sessions and dialogue.** A conversation belongs to one client. A step that
continues on another client starts a fresh conversation with the full prompt, plus
every round of questions it asked in this visit with the person's answers, so nothing
the person said is lost.

**What is recorded.** Every choice is in the chronicle with the attempt's
`step_started`: the role, the mode, the client's position, the reason (`fixed`,
`turn`, `quota` or `resume`), the client whose limit made the step change client, and
what was known of every client of the role (each window's share, where it came from,
when it resets, the attempts and time Hetman saw, and whether the client could run and
until when). When no client can run, a `no_client` event records the same and the
time the campaign waits for. `hetman status`, the step card on the page and `hetman
scribe explain` show which client ran each attempt and why; the page's Roles tab shows
every role of the served orders with each client's current usage per window, measured
(as of the last time an adapter was asked) or estimated, and which clients are
exhausted until when.

`examples/roles` shows the three modes with two stub clients: round robin, selection
by remaining subscription, and a change of client after a limit.

## Questions to a person

A flow can stop and ask a person in three ways: a gate with questions, a human node,
and an agent step in dialogue. While it waits, the campaign is in a state of its own,
recorded in the chronicle: it survives a restart, and nothing has to keep running.

```yaml
notify: {run: [./notify.sh], timeout: 30s}   # optional, see Notification hook

nodes:
  plan_approved:
    type: gate
    checks: [format]          # optional; run first
    ask:
      - id: scope
        question: Does the plan cover every acceptance criterion?
      - id: risk
        question: Is the migration reversible?
    show: [plan.md, "{{work}}/notes.md"]   # files the person looks at before answering
    on: {pass: implement, blocked: plan}
  release:
    type: human
    ask:
      - {id: channel, question: "Which channel?", kind: choice, options: [stable, beta]}
      - {id: notes, question: "Anything to mention?", kind: text}
      - {id: urgent, question: "Is it urgent?"}            # kind yesno is the default
    show: [CHANGELOG.md]
    on: {ship: publish, rework: implement, stop: failed}
  interview:
    type: agent
    dialogue: 3               # may ask the person up to 3 rounds of questions
    ...
```

**Questions.** `ask` lists questions, each with an `id` (letters, digits, `_`, `-`,
`.`), the `question`, and a `kind`: `yesno` (the default), `choice` (with `options`,
at least two) or `text`. A person may add a comment to any yes/no or choice answer.
`show` lists files, relative to the orders folder, that the person is pointed to (in
the terminal) or can read (on the page) before answering. An entry that starts with
`{{work}}/` names a file in the work folder of the campaign that asks instead, as in
`show: ["{{work}}/plan.md"]` for a plan an agent step wrote there; it is resolved per
campaign, so two campaigns, or the children of a segment or for-each, each show their
own file. Either way an entry must stay inside its folder (`..` is rejected).

**Gate with questions.** A gate may have `ask` with or without `checks` (not with
`run`). The checks run first; only if they let the gate pass is the person asked.
The person answers yes or no to each question. Any `no` blocks the gate; all `yes`
passes it. The node the flow goes to next gets the answers through `{{rework}}` when
the flow went back to it, and any later prompt through `{{answers.NODE...}}`.

**Human node.** A `human` node has no command. Its outcomes are the keys of its `on`,
and the person chooses one of them; with a single key it is chosen without asking.
The questions (`ask`, optional) are input for later steps, not a vote: they never
decide the outcome by themselves.

**Dialogue.** An agent node with `dialogue: N` may ask the person questions in the
middle of its work, at most N rounds per visit of the node. Hetman adds to its prompt
how to ask, and gives the adapter a schema that also allows the answer
`{"outcome": "questions", "questions": [{"id": "...", "question": "...", "kind": "yesno"}]}`
(kinds as above; `choice` with `options`; one to 20 questions). Such an answer is a
round of questions, not a result: it uses no attempt, and Hetman waits for the
person. Then it continues the same conversation: the next attempt gets the session
id the step reported and, as its prompt, only the person's answers. After the last
round the step is told it may not ask again, and the schema no longer allows
questions; asking anyway is an answer that does not match the schema. The outcome
`questions` and the property `questions` are reserved in the schema of such a node.
Continuing a conversation needs an adapter that declares the `resume` feature; with
any other adapter the campaign stops in the error state before the step runs, and a
step that asks without reporting a session id fails that attempt.

**Answering.** `hetman status` shows what is asked, the files to look at and the
commands that answer. `hetman answer` asks each question in the terminal. With flags
it answers without asking, for scripts and chat bots:

```sh
hetman answer -c 3 -a scope=yes -a risk=no -comment risk="drops a column" -by alice
hetman answer -c 3 -a channel=beta -a notes="thanks" -a urgent=n -outcome ship
```

`-a ID=ANSWER` answers one question (`yes`/`no`, also `y`/`n`; an option; or text),
`-comment ID=TEXT` adds a comment, `-outcome NAME` chooses the outcome of a human node,
`-ask N` answers only if question #N (the number `status` shows) is still the open one,
and `-by NAME` says who answers (the user name of the computer by default). Every
question needs an answer; answers that do not fit are refused and nothing is recorded.
Only the first answer to a question counts. Every answer is a chronicle event with its
time, who gave it and from where.

**Waiting.** `hetman run` (or `resume`) keeps running while it waits, looking at the
chronicle every second, and goes on as soon as an answer is recorded, whoever recorded
it. Halt works while it waits. If the process ends while the campaign waits (Ctrl-C,
a crash), the question stays open; answer it, then `hetman resume` continues. An
answer given on the Watchtower page to a campaign that no process drives is
continued by `hetman serve` itself.

**Notification hook.** `notify` names a command that Hetman runs whenever a campaign
starts waiting for a person: a question (reason `gate`, `human` or `dialogue`), lost
connectivity (`offline`), a subscription limit with no time to retry (`limit`), or the
error state (`error`). It runs in the folder of the orders file, with the variables of
a step (`HETMAN_RUN_ID`, `HETMAN_NODE_ID`, `HETMAN_RUN_DIR`, `HETMAN_INPUT_FILE`, ...) plus
`HETMAN_REASON`, and reads one line of JSON on its standard input:

```json
{"campaign": 3, "flow": "release", "orders": "/path/to/orders.yaml", "node": "plan_approved",
 "reason": "gate", "message": "gate plan_approved asks 2 question(s)", "event": 17,
 "at": "2026-11-14T10:00:00Z",
 "questions": [{"id": "scope", "question": "Does the plan cover ...?", "kind": "yesno"}],
 "outcomes": [], "show": ["/path/to/plan.md"],
 "command": "hetman answer -f /path/to/orders.yaml -c 3"}
```

`outcomes` is there for a human node, `questions` and `show` for a question, and
`command` is the command that answers (for a question) or resumes (otherwise); `event`
is the number to pass as `-ask`. For a campaign the dispatcher started (see Tasks and
the dispatcher), `task` names its task: `{"id": 7, "kind": "reviewer-remark", "key": "..."}`. The campaign never waits for the hook: it runs in the
background and is killed after `timeout` (30 seconds by default). Its output goes to
`logs/notify.EVENT.log` in the campaign folder and how it ended to a `notified` event.
A process that ends waits for hooks it started, up to their timeout. Any messenger can
be connected with a small script that sends the message and, when the person replies,
runs the `hetman answer` command with flags.

## Segments, for-each and locks

**Segments.** A piece of a flow is written once, in its own orders file, and used as
one step of other flows:

```yaml
nodes:
  review:
    type: segment
    segment: segments/review.yaml   # an orders file, relative to this one
    input: output.plan.task         # optional; this campaign's own input by default
    on: {done: publish, failed: failed}
```

The node runs the segment as a child campaign, driven by the same engine in the same
process, and takes the end the child reached as its outcome. `input` is what the
child gets as its run input: `input` or `input.FIELD` of this campaign,
`output.NODE[.FIELD]` of an agent node, or `artifact.NODE.FILE`, a JSON file a script
or gate listed as an artifact. It must be a JSON object. The child keeps the language
for the person of its parent. Steps of a segment run in the folder of the segment's
file and resolve their paths there; segments may run segments, up to 8 levels deep.
`hetman check` reads every segment, reports a problem in one with that file's line,
and rejects a segment that runs itself, directly or through others.

**For each.** A `foreach` node runs a segment once per item of a list:

```yaml
  build-all:
    type: foreach
    segment: segments/item.yaml
    items: output.plan.items        # or artifact.NODE.FILE, or input.FIELD
    parallel: 2                     # how many items at the same time; 1 by default
    on: {done: release, failed: plan}
```

The list is a JSON array of objects. Each has an `id` (letters, digits, `_`, `-`,
`.`), unique in the list, and optionally `needs`, the ids of the items that must
reach `done` before it starts; the whole object is the run input of its child.
Items whose needs are met run at the same time, up to `parallel`. An item that fails
does not stop the others; an item that needs a failed item, directly or through
another, is not run and the chronicle says why. The node ends once every item ended
or can no longer run: `done` when every item reached `done`, else `failed`. A list
that is not an array of such objects, has the same id twice, needs an id it does not
have, or whose needs form a cycle makes the node fail at once with the reason, so
`failed` can lead back to the step that wrote it. The list is recorded when the node
starts, so a resumed campaign runs the same list. The input of one item cannot be
changed after that: change what writes the list (the step's prompt, its script, or
the run input it reads), then `hetman rewind -to NODE` the for-each node to read the
list again, or rewind to the step that writes it to have it written again. A
`hetman retry` of the for-each node keeps the recorded list.

**Children.** Each child is a campaign of its own: its own events in the chronicle,
its own folder under `.hetman/campaigns/`, its own attempts. The parent records
which child it started for which item and how each ended. Children are not listed
on their own; you reach them through their parent:

- `hetman status` lists the children of a segment or for-each node under its row,
  with their state, the items not run and why, and the children of children; a
  child that waits for a person, for a lock or after an error is named in the state
  line. `hetman status -c CHILD` shows one child in full.
- `hetman answer` on the parent answers whatever a child asks (`-ask N` picks one
  when several children ask at once); `hetman halt` and `hetman resume` act on the
  parent and reach every running child; asked of a child, they refuse and name its
  parent.
- A halt takes effect in each running child after its current step, then in the
  parent. Resume continues the parent, which continues each unfinished child where it
  stopped; children that already ended are never run again. After a crash, resume
  does the same, and the step that was running in a child runs again there, as in any
  campaign.
- A child waiting for a person makes its parent wait with it: a question keeps
  `hetman run` waiting until it is answered; a child stopped in the error state
  stops its parent in the error state (exit code 4) once the other children have
  gone as far as they can; a child stopped by a subscription limit or lost
  connectivity makes the parent end with exit code 5. The notification hook of the
  orders a person started runs for every child, with `child` and `path` in its
  notice.

**Locks.** A node, or a check, may name resources it needs exclusively:

```yaml
checks:
  e2e: {run: [sh, checks/e2e.sh], locks: [test-stand]}
nodes:
  test:
    type: script
    run: [sh, steps/test.sh]
    locks: [test-stand, writer]
    on: {ok: done, fail: failed}
```

Two steps holding the same lock never run at the same time: in one campaign,
between the children of a for-each, and between campaigns and `hetman` processes
that use the same `.hetman` folder. A step waits for its locks before it starts; the
wait is recorded with the name of the lock and of who holds it, and `hetman status`
and the page show it. A step takes its locks one at a time in name order, holds them
until it ends and gives them back; a gate also takes the locks of its checks, for
the whole gate. A segment or for-each node holds its locks while its children run,
so a lock taken inside a segment must sort after the locks of the node that runs it,
which `hetman check` enforces: with every holder taking locks in one order, no two
can wait for each other. A lock is an exclusive lock on a file in `.hetman/locks/`,
which the system drops when its process ends, however it ends: a killed engine
leaves no lock behind, and a halt releases the locks with the step.

## Adapters

An adapter is the program between Hetman and one coding-agent client: it receives
one step, runs the client on it, and reports what the client does and answers, in
the protocol described under Writing an adapter. The `hetman` binary contains no
adapter and never names a client; an orders file names the adapter program of each
executor. This repository has three: `hetman-adapter-claude` for the Claude Code
client, `hetman-adapter-opencode` for the OpenCode client, and
`hetman-stub-adapter`, which runs no model (see Agent steps without a real client).

### Claude Code

`hetman-adapter-claude` runs a step through the `claude` command-line client in
print mode, on your subscription. Download `hetman-adapter-claude-VERSION-OS-ARCH`
for your system from the Releases page (it is in the same checksum file), or build
it:

```sh
go build -o hetman-adapter-claude ./cmd/hetman-adapter-claude
```

It needs `claude` on the path and logged in (`claude auth login`; on a computer
without a browser, a token from `claude setup-token` in `CLAUDE_CODE_OAUTH_TOKEN`).
Name it under `executors`:

```yaml
executors:
  claude: {run: [./bin/hetman-adapter-claude]}   # options: --client PATH, --env NAME

nodes:
  implement:
    type: agent
    executor: claude
    model: sonnet
    effort: high
    tools: [Read, Edit, Write, Grep, Glob, "Bash(go test *)"]
    ...
```

For each attempt it runs the client in the step's `workdir`, with the prompt on
standard input:

```
claude -p --output-format stream-json --verbose --restricted --strict-mcp-config
  --permission-mode dontAsk --permission-prompts none
  --tools=Read,Edit,Write,Grep,Glob,Bash --allowedTools=Read ... --allowedTools=Bash(go test *)
  [--add-dir WORK] --disallowedTools=Edit(//PROTECTED) --disallowedTools=Edit(//PROTECTED/**) ...
  --model sonnet --effort high --json-schema SCHEMA [--resume SESSION]
```

- `model` is a model alias of the client (`haiku`, `sonnet`, `opus`) or a full model
  name; empty means the client's default. An unknown model fails the step at once.
- `effort` is `low`, `medium`, `high`, `xhigh` or `max`; empty means the client's
  default. The adapter rejects any other value, which the client would ignore.
- `tools` lists client tool names (`Read`, `Write`, `Edit`, `Glob`, `Grep`, `Bash`,
  `WebFetch`, `WebSearch` and the others of the client's built-in set) and permission
  rules such as `Bash(git status)` or `Bash(go test *)`. Only the tools named are
  available, each entry is approved in advance, and anything else is denied without
  asking anybody. No tools means none. The adapter never uses an option that skips
  permission checks. In `--restricted` mode the client ignores the user, project and
  local settings files (their permission rules, hooks and tool servers do not apply)
  and its file tools stay inside the working folders: `workdir` and, when it lies
  outside, the campaign's work folder.
- A file path in a rule is matched by the client only in `Edit(...)` rules, which
  cover every tool that changes files (`Edit`, `Write`, `MultiEdit`, `NotebookEdit`),
  and in `Read(...)` rules, which cover the tools that read files. The client accepts
  `Write(path)`, `MultiEdit(path)`, `NotebookEdit(path)` and `Glob(path)` and then
  never matches them, so every such call would be denied; the adapter rejects them at
  once as a `permanent` error that names the entry and the rule to write instead
  (`Write(plan.md)` becomes `Edit(plan.md)`). It also rejects an entry that does not
  end at its closing parenthesis, empty parentheses, a lower-case tool name, the
  `:*` form outside Bash rules and tools of tool servers, which are not loaded. An
  `Edit(path)` rule makes `Write` available too, approved for the same paths, since
  `Write` is the tool that creates files. `hetman check` reports the same problems at
  their line, through the adapter's `check` feature.
- Protection: each protected path gets an `Edit` deny rule for itself and everything
  inside it. The client's deny rules always win over its allow rules, so a protected
  folder that holds the writable work folder (`.hetman/` holds `campaigns/N/work/`)
  is denied entry by entry around the way to it: the chronicle, the other campaigns'
  folders, this campaign's `logs/`, `results/`, `prompts/` and lock. Entries created
  there after the attempt starts are not covered. The client enforces this for its
  file tools only; `Bash` commands allowed by `tools` are not restricted by it.
- A limit of the client the adapter cannot change: whenever `Bash` is available (any
  `Bash` or `Bash(...)` entry), the client also runs, without a matching entry,
  commands it judges read-only, such as `ls`, `cat`, `head`, `tail`, `grep`, `find`,
  `wc`, `diff` or `stat`, also chained with `&&` or `|`; no option of print mode
  turns this off, and a deny rule cannot carve them out, since it would also deny
  the allowed entries. The adapter says so in the step's log when Bash is available.
  A compound command such as `cd work && make` is checked as a whole and is denied
  unless an entry matches it; write `Bash(make -C work *)` instead.
- The answer is the client's structured output, checked by the client against the
  node's schema and again by Hetman. When the client gives only text, the last JSON
  object in it is taken; no object at all is a transient error.
- Errors: a subscription limit is `limit`, with the reset time when the client gives
  one; a failed connection is `offline`; not logged in, an unknown model and bad
  arguments are `permanent`; anything else is `transient`.
- The log shows what the client does: its text, each tool call and what it was
  called on, and the first line of each tool result. A call the client denied is
  listed at the end as `denied: TOOL TARGET: WHY; the client said: REASON`, where WHY
  names the step's entries for that tool that it matched none of (or that the tool is
  not among the step's tools), and the calls are reported with the answer (see Denied
  tool calls). An answer without a JSON object after a denied call is a `permanent`
  error that lists the denied calls, since a retry would meet the same denial.
- The client gets only the environment it needs: the system's basic variables,
  proxy and certificate settings, `CLAUDE_CONFIG_DIR` and `CLAUDE_CODE_OAUTH_TOKEN`.
  `--env NAME` adds one more. `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` are never
  passed, since they would switch the client to paid API usage. Because the
  variables of a running client session are not passed either, Hetman can itself be
  run from inside a Claude Code session.

**Session resume.** The adapter declares `resume` and reports the client's session
id as soon as the client starts. When Hetman gives it a session id, the client
continues that conversation with `--resume`, keeping the same id, and receives the
whole prompt of the new attempt as the next message. The client keeps conversations
on the computer that ran them, under its configuration folder and the working folder,
so a session can be continued only by the same user on the same computer for the same
orders folder. When the client cannot find the session, the adapter says so in the
log and starts a new conversation with the prompt.

**Usage.** The adapter does not declare the `usage` feature. The installed client
has no command, flag or status file meant for programs that reports how much of the
subscription is used without a model request: `claude auth status` reports the
account and plan, not usage; the usage shown inside an interactive session and the
per-window utilization in the `rate_limit_event` lines of print mode exist only while
a request runs. A role's `quota` mode therefore estimates this client from the
attempts Hetman saw against the client's `allowance`, and the limit errors it reports
(with their reset times) still keep it from being chosen until the reset.

`examples/claude` is a small flow with this adapter: a draft step that writes a note
into a file, a gate that checks the file, and a review step that may send the draft
back once. Its comments say how to run it; a run makes two to four small requests
with the `haiku` model.

### OpenCode

`hetman-adapter-opencode` runs a step through the `opencode` command-line client in
non-interactive mode (`opencode run`). Download
`hetman-adapter-opencode-VERSION-OS-ARCH` for your system from the Releases page (it
is in the same checksum file), or build it:

```sh
go build -o hetman-adapter-opencode ./cmd/hetman-adapter-opencode
```

It needs `opencode` on the path with a provider it can use: one of its free models, or
a provider you logged in to with `opencode providers login` (`opencode models` lists
what it can use). Name it under `executors`:

```yaml
executors:
  opencode: {run: [./bin/hetman-adapter-opencode]}   # options: --client PATH, --env NAME

nodes:
  implement:
    type: agent
    executor: opencode
    model: opencode/big-pickle
    effort: high
    tools: [read, edit, glob, grep, "bash(go test *)"]
    ...
```

For each attempt it runs the client in the step's `workdir`, with the prompt on
standard input:

```
opencode run --format json --pure --print-logs --log-level INFO --agent hetman-XXXXXXXX
  --model PROVIDER/MODEL --variant EFFORT (--session SESSION | --title TITLE)
```

and gives it, in `OPENCODE_CONFIG_CONTENT`, an agent made for this run only whose
permissions deny everything, then allow the node's tools, then deny writing the
protected paths.

- `model` is a model in the client's `provider/model` notation, as `opencode models`
  lists it; empty means the client's default. An unknown model, or a provider the
  client has no login for, fails the step at once.
- `effort` is passed as the model variant, the client's only reasoning setting.
  Variant names differ per model (`opencode models PROVIDER --verbose` lists them).
  The adapter looks the model up in the client's catalogue first and rejects a value
  the model does not offer, and any value for a model without variants. Without a
  model the value is passed unchecked.
- `tools` lists client permission names (`read`, `edit`, which also covers writing
  and patching files, `glob`, `grep`, `list`, `bash`, `webfetch`, `websearch`,
  `todowrite`, `lsp`, `skill`, or a tool server's tool name) and rules such as
  `bash(git status)` or `read(*.md)`, which allow a tool only for what matches the
  pattern. Every other tool is denied and not offered to the model, and the client
  never asks anybody in this mode. No tools means none. The adapter never uses an
  option that skips permission checks (`--auto`), and its file tools stay inside
  `workdir` (outside folders are denied as external directories, except the
  campaign's work folder). `task` is refused, because the subagents it starts run with
  the client's default permissions. The client governs writing files with `edit`
  alone, so `write`, `patch`, `apply_patch` and `multiedit` would never match and are
  rejected, as are a pattern on `todowrite`, `webfetch` or `websearch`, which take
  none, and `external_directory`, `doom_loop` and `question`, which the adapter sets
  itself; each as a `permanent` error naming the entry, and by `hetman check` at its
  line. External plugins are not loaded (`--pure`). The
  restriction has limits: tool servers (MCP) from your or the project's configuration
  are still started, although their tools are denied unless named, and an
  organisation's remote or managed configuration is applied after the adapter's and
  can loosen it.
- The answer: the client has no structured output in this mode, so the adapter appends
  the node's schema to the prompt and asks for one JSON object at the end of the
  answer. The last JSON object in the answer is taken and checked by Hetman; no object
  at all is a transient error.
- Errors: a provider's usage or plan limit is `limit`, with the reset time when the
  provider gives one; the adapter stops the client as soon as its log reports a limit
  instead of letting it retry. A failed connection is `offline`; an unknown model or
  provider, missing credentials and bad arguments are `permanent`; anything else is
  `transient`.
- Protection: the `edit` permission denies each protected path and what is inside
  it after the node's own rules, written relative to `workdir` and to every folder
  above it, because the client matches a path relative to its worktree (the root of
  the git repository holding `workdir`, or the root of the file system outside one).
  The client applies the last rule that matches, so when the node may edit everywhere,
  the work folder is allowed again after the denials and new files in `.hetman/` are
  covered too; when the node may edit only some patterns, the protected paths are
  denied entry by entry around the work folder, as for Claude Code. The client
  enforces this for `edit`, `write` and `apply_patch`; `bash` commands allowed by
  `tools` are not restricted by it. The client's patterns know only `*` and `?`, so
  a protected path holding either matches more than itself.
- The log shows what the client does: its text, each tool call and what it was
  called on, the first line of each tool result or error, and the model steps and
  tokens at the end. A call the client's permissions refused is logged as
  `denied: TOOL TARGET: REASON` and reported with the answer (see Denied tool calls),
  and an answer without a JSON object after one is a `permanent` error.
- The client gets only the environment it needs: the system's basic variables, its
  configuration and data folders (`XDG_*`), where it finds its stored logins, and
  proxy and certificate settings. Provider API keys in the environment are not
  passed. `--env NAME` adds one more.

**Session resume.** The adapter declares `resume` and reports the client's session
id as soon as the client creates the session. When Hetman gives it a session id, the
client continues that session with `--session` and receives the whole prompt of the
new attempt as the next message. The client keeps sessions in its data folder on the
computer that ran them, so a session can be continued only by the same user on the
same computer. When the client cannot find the session, the adapter says so in the
log and starts a new conversation with the prompt.

**Usage.** The adapter does not declare the `usage` feature. `opencode stats` counts
tokens and cost per day from the client's own records, but no share of a provider's
allowance and no reset time, in a table meant for people; nothing else the client
offers reports either without a request. A role's `quota` mode therefore estimates
this client from the attempts Hetman saw against the client's `allowance`.

`examples/opencode` is the same small flow as `examples/claude` with this adapter.
Its comments say how to run it; a run makes two to four small requests and needs a
provider with quota left. A step that hits the provider's limit makes the campaign
wait until `hetman resume`.

## Writing an adapter

An adapter is a program that runs one agent step through some client. Hetman starts
one adapter process per attempt, in the folder of the orders file, with the same
environment and `HETMAN_*` variables a script gets (without `HETMAN_RESULT_FILE`).
Messages are JSON objects, one per line: Hetman writes to the adapter's standard
input, the adapter writes to its standard output. Standard error is the adapter's
own diagnostics; Hetman saves it and never reads it. This is protocol version 0.

**Describe.** Before the first step of an executor, Hetman runs the program with
the extra argument `--describe`. It must print one line and exit with code 0:

```json
{"protocol": [0], "name": "my-adapter", "features": ["resume"]}
```

`protocol` lists the versions it speaks and must include 0. The features are
`resume`, which dialogue steps need; `usage`, which the `quota` mode of a role
reads; `protect`, which says the adapter keeps the client's file tools off the paths
in `protect` (below); and `check`, which says it can check steps without running
them.

**Usage.** An adapter that declares `usage` answers the extra arguments `--usage
five_hour weekly` with one line and exit code 0, without making a model request: its
client's share of each window asked for that the client can tell (0 nothing used, 1
all of it) and when the window resets, if known. A window the client does not report
is left out; a client that counts by month reports that share as `weekly`.

```json
{"windows": [{"name": "five_hour", "used": 0.42, "resets_at": "2026-11-15T18:00:00Z"}, {"name": "weekly", "used": 0.1}]}
```

Report only what the client itself measures; when it has no such number, do not
declare the feature, and Hetman estimates from what it saw.

**Start.** Hetman then starts the program without `--describe` and writes:

```json
{"v":0,"type":"start","step":{"run_id":"1","node_id":"implement","attempt":1,
 "prompt":"...","model":"...","effort":"...","tools":["read","edit"],
 "workdir":"/path/to/project","schema":{...},"session":null,
 "protect":["/path/to/project/flow/.hetman","/path/to/project/flow/orders.yaml","..."],
 "writable":["/path/to/project/flow/.hetman/campaigns/1/work"]}}
```

`model`, `effort` and `tools` are the strings from the orders (empty when not set).
`workdir` is the node's working folder; run the client there and keep its file tools
inside it. `schema` is the node's schema with the reserved outcome `cannot_proceed`
and its field `cannot_proceed_reason` added. `session` is null, or the id to
continue. `protect` lists absolute paths the client's file tools must not write, with
everything inside them; `writable` lists folders that must stay readable and
writable although they lie inside a protected path or outside `workdir`. An adapter
whose client can enforce this declares `protect`. A tools entry the client would
accept and never honour should be a `permanent` error that names it.

**Check.** An adapter that declares `check` is run by `hetman check` with the extra
argument `--check` and gets one line listing the agent nodes that use it:

```json
{"v":0,"type":"check","steps":[{"node_id":"implement","model":"...","effort":"...","tools":["Read","Write(plan.md)"],...}]}
```

Without contacting a model it prints one line and exits with code 0:

```json
{"v":0,"type":"checked","problems":[{"node_id":"implement","field":"tools","item":1,"message":"..."}]}
```

`field` is `model`, `effort` or `tools`, `item` the index of a tools entry from 0.
Hetman reports each problem at its line of the orders file and rejects the file.

**Messages from the adapter.**

```json
{"v":0,"type":"log","text":"..."}
{"v":0,"type":"session","id":"..."}
{"v":0,"type":"result","output":{"outcome":"ok"}}
{"v":0,"type":"error","kind":"transient","message":"..."}
```

- `log` lines are shown live as the step's log.
- `session` reports the id of the conversation; send it only with the `resume`
  feature.
- `result` may carry `"denied"`: the tool calls the client denied during the step,
  each as one line naming the tool, what it was called on and the reason. Hetman
  records them with the attempt and shows them in `hetman status`, the explanation
  and the step card; an agent node with `fail_on_denied: true` fails such an
  attempt.
- `result` carries the answer. Hetman, not the adapter, checks it against the
  schema.
- `error` has `kind` `transient` (worth retrying), `permanent` (retrying cannot
  help), `limit` (subscription limit; add `"retry_at"` with an RFC 3339 time when the
  client says when it resets) or `offline` (no connectivity).
- Exactly one `result` or `error` ends the step, after which the adapter exits with
  code 0 within five seconds. Exiting without one, with another code, or writing a line that is not one
  of these messages counts as a failed attempt. Messages after the first `result`
  or `error` are ignored.
- Hetman may write `{"v":0,"type":"cancel"}` when it is stopped; the adapter should
  stop and exit. After a few seconds Hetman kills its process tree.

`internal/adapter/adaptertest` is a conformance suite any adapter must pass. Run it
against yours with:

```sh
HETMAN_ADAPTER='["/path/to/my-adapter"]' go test ./internal/adapter/adaptertest -run External -v
```

It sends tiny steps: one, one resumed and a dialogue of two steps (both only for an
adapter that declares `resume`), and one that is cancelled, and asks for the usage
(only for an adapter that declares `usage`; no model request); `HETMAN_ADAPTER_MODEL` and
`HETMAN_ADAPTER_EFFORT` set the `model` and `effort` of those steps, so that a real
client can be checked with its cheapest model. `cmd/hetman-stub-adapter` is an
adapter that passes it without any client; its plan format is described in
`internal/stubadapter`. The tests of `cmd/hetman-adapter-claude` run the suite
against that adapter with a fake client, and against the real client only when asked
to (three small requests with the `haiku` model):

```sh
HETMAN_CLAUDE_REAL=1 go test ./cmd/hetman-adapter-claude -run RealClient -v
```

## Commands

```
hetman check  [-f FILE]                 check the orders without running anything
hetman run    [-f FILE] [-input FILE]   start a new campaign and run it
hetman status [-f FILE] [-c ID] [-events]  show where a campaign is
hetman halt   [-f FILE] [-c ID]         halt a campaign after its current step
hetman resume [-f FILE] [-c ID]         continue a halted, stopped or interrupted campaign
hetman retry  [-f FILE] [-c ID] [-by NAME]  run the failed step again with a fresh attempt budget
hetman rewind -to NODE [-f FILE] [-c ID] [-by NAME]
                                        send a campaign back to a node it visited
hetman answer [-f FILE] [-c ID] [-a ID=ANSWER ...] [-comment ID=TEXT ...] [-outcome NAME] [-ask N] [-by NAME]
                                        answer what a campaign asks a person
hetman serve  [-f FILE] [-addr ADDRESS] show every campaign live on a web page (Watchtower)
hetman task add    [-r FILE] -kind KIND [-key KEY] [-input FILE|-] [-force]
                                        queue an internal task (see Tasks and the dispatcher)
hetman task list   [-r FILE] [-state STATE]   list the tasks with their states
hetman task show   [-r FILE] ID         show one task, its campaign and its input
hetman task cancel [-r FILE] ID         cancel a queued task
hetman task check  [-r FILE]            check the routing file and every flow it routes to
hetman dispatch    [-r FILE] [-once] [-limit N] [-poll D]
                                        run a campaign of the routed flow for each queued task
hetman scribe [-f FILE]                 the setup wizard (see Setup assistant)
hetman scribe OPERATION ARGS [-f FILE] [-dry-run] [-force]
                                        one operation on the orders file
hetman scribe [-f FILE] [-yes] "SENTENCE"
                                        one sentence to one operation or question, through a model
hetman scribe explain [-f FILE] [-c ID] [-kind KIND] [-node NODE]
                                        explain a campaign in plain words
hetman scribe skill [-dir DIR] [-force] write the setup assistant skill for a coding agent
hetman version                          print the version of hetman
```

- `-f FILE` is the orders file, `orders.yaml` in the current folder by default.
- `-input FILE` is a JSON file holding one object, the run input of the campaign.
- `-c ID` is the campaign number; the latest campaign by default.
- `-events` makes `status` also list every chronicle event.
- `-to NODE` is the node `rewind` sends the campaign back to; `-by NAME` records who
  asked for a retry or rewind (the user name of this computer by default).
- `-addr ADDRESS` is where `serve` listens, `127.0.0.1:8740` by default.
- `-r FILE` is the routing file of `task` and `dispatch`, `routes.yaml` in the current
  folder by default.
- `HETMAN_LANGUAGE=CODE` in the environment of `check` and `run` overrides the
  `language` key of the orders (see Language for the person). For every command it
  is also the language of Hetman's own messages (see Interface language).
- `-dir DIR` is the folder `scribe skill` writes `SKILL.md` into,
  `.agents/skills/hetman-scribe` in the current folder by default. `-force` replaces
  a different `SKILL.md` that is already there.

`scribe skill`, `scribe new` and `version` need no orders file.

`hetman check` prints `FILE: ok (...)` or one line per problem as
`FILE:LINE:COLUMN: message`; a problem in a prompt template or a schema names that
file. Besides the file itself, it looks for the program of
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
| 5 | an agent step, here or in a child campaign, hit a subscription limit with no time to retry, or lost connectivity; waiting for a person |

A campaign waiting for answers does not end `run` or `resume`; they wait with it.
`hetman answer` exits with 0 when the answers are recorded, 2 when they do not fit
(nothing is recorded) and 1 when nothing is open to answer.
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
| `HETMAN_INPUT_FILE` | Path of `input.json` in `HETMAN_RUN_DIR`: the run input, `{}` without one |
| `HETMAN_LANGUAGE` | Code of the campaign's language for the person; not set when it has none |

Earlier steps leave files for later ones in `HETMAN_RUN_DIR`: `outputs/NODE.json` (the
accepted output of an agent node) and `answers/NODE.json` (what a person answered at
a gate or human node).

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
artifacts, a timeline, and the chronicle events. For an agent step the card shows
the executor, model and effort, the prompt of each attempt, the adapter's log live
while it runs, its diagnostics, the accepted answer, the session id, and why a
failed attempt failed. A campaign waiting for a subscription limit, or stopped by
one or by lost connectivity, is marked as such. A campaign waiting for a person is
marked "waiting for a person", and so is the tile of the step that asks. Everything on it comes from the
chronicle, as with `hetman status`, and updates while campaigns run.

A segment or for-each step shows one tile per child with the child's own state, where
it is and what it waits for (a person, a lock), and a tile for each item not started
or not run, with the reason. A child's tile opens the child's own page, which links
back to its parent; its Halt and Resume are those of its parent. A question asked in
a child shows on the parent's page too, with a link to the card that answers it.

When the folder has a task queue (see Tasks and the dispatcher), a Tasks tab lists the
tasks with their kind, state, campaign (a link), age and what a waiting one waits for.
When the served orders declare roles, a Roles tab lists each role with its mode and
each client with its executor, model and effort, its share of the five-hour and the
weekly window (measured, with the time it was measured, or estimated, with the
allowance and the attempts seen), and whether it can run or is exhausted until when.
It is read-only and asks no adapter anything: measured shares are those recorded with
the last choice. The flow editor has a small form for roles (declare one, change its
mode, add or remove a client), and an agent node's settings can point it at a role.
A campaign of another orders file of the same folder is shown with its own nodes.


It has the controls Halt, Resume and Retry, which do what `hetman halt`,
`hetman resume` and `hetman retry` do, a Rewind button on the card of every step the
campaign has visited, and an answer form. A campaign resumed, retried, rewound or
started from the page is run by the `hetman serve` process; stopping that process
interrupts it, and `hetman resume` continues it. The pipeline counts every attempt of a
step across retries and rewinds, shows how much of the attempt budget the current
agent step has used in this visit, and marks the steps that ran before the latest
retry or rewind and run again when the flow reaches them; the card of a step says who
sent the campaign back to it and when, and the plain-words tab tells it too.

**Starting a campaign.** The list of campaigns has a Start button. It offers the
orders file the server was started with and every other orders file in the same
folder that passes the check, shows the language for the person the campaign will
get, and takes the run input as a JSON object typed or pasted into the form. The page
checks that it is one object before sending it, and remembers the last input of each
orders file in that browser. The server checks the orders and their programs as
`hetman run` does; their problems are shown instead of starting.

**Editing the flow.** Each orders file of the folder has a Flow view: its pipeline,
and for each node the operations of `hetman scribe` that make sense there (add a step
after or before, add a gate, remove, rename, change or remove a transition, set the
timeout, change a setting of an agent step, add a question, add a check to a gate or
remove one, mark a check soft or required), each as a small form filled with the
node names, checks and executors of the file. Every change is shown first, in plain
words and as the diff `hetman scribe` prints, with the problems of the orders check;
nothing is written until the person confirms, and a change whose result does not
pass the check cannot be written. The change is made again on the file when it is
written, and refused if the file changed since it was shown. When the orders name an
executor and model for Scribe, a line takes one sentence and shows the operation it
becomes for confirmation, or the interpretations to choose from when it is unclear;
the sentence goes to the model only when the person presses the button. Campaigns of
the file that have not ended read the changed file when they continue, and the page
says so before the change is written.

**Safety.** Everything that changes state (halt, resume, answer, start, retry, rewind,
the previews, writing a change, a sentence to Scribe) needs a request from the page
itself: the browser's `Origin` and `Sec-Fetch-Site` headers must name the server and
the `X-Hetman-Control` header must be set, which a form or a script on another site
cannot do. The page writes only the orders files of the server's folder, the starter
prompt and schema files an operation creates inside that folder, and the prompt
templates of the flow's agent steps that a rename changes; never a file outside the
folder or under its `.hetman` directory. It never creates a new orders file.

The card of a step that asks shows the files to look at, the questions, and, while
they are open, a form: yes or no and a comment for each question of a gate; the
questions of a human node and the choice of its outcome; the questions of an agent
step in dialogue. It also shows the `hetman answer` commands that do the same.
Submitting the form goes through the same checks as `hetman answer`: answers that do
not fit are shown in the form and change nothing. Recorded answers are continued by
the process that drives the campaign, or by `hetman serve` when none does. Once
answered, the card shows the answers, who gave them and when.

## Tasks and the dispatcher

Some work is too small for a ticket: a linter reported a finding on a commit, a
reviewer left a remark. Hetman keeps its own queue for such tasks. Something outside
(a scheduler, a script that watches a code review system) adds a task with one
command; the dispatcher picks the flow by the task's kind and runs the task as an
ordinary campaign of that flow. The work is always a campaign: it shows in
`hetman status` and Watchtower, can be halted, resumed and answered, and follows its
orders like any other.

**Routing.** `routes.yaml`, in the same folder as the orders files it names, maps each
kind of task to a flow:

```yaml
version: 0
limit: 2                  # campaigns running at once over all routes (default 1)
routes:
  machine-finding:
    orders: finding.yaml
    priority: 10          # higher first (default 0)
    limit: 1              # campaigns of this route at once (default: no limit of its own)
  reviewer-remark:
    orders: remark.yaml
```

Kinds use letters, digits, `_`, `-` and `.`. Every routed orders file must be in the
folder of the routing file: the flows share the chronicle and the queue in its
`.hetman/` (`chronicle.db` and `tasks.db`). `hetman task check` checks the routing file
like `hetman check` checks orders, with `FILE:LINE:COLUMN` for unknown keys, bad values
and missing orders files, and checks every routed orders file too; their problems are
listed under the route that names them.

**Adding tasks.** `hetman task add -kind KIND -input FILE` queues a task and prints its
number; `-input -` reads the input from standard input, and without `-input` it is
`{}`. The input is one JSON object. A kind with no route is refused and nothing is
added. `-key KEY` gives a de-duplication key: while any task with the same key exists
that was not cancelled, whether it is queued, running, waiting, finished or failed,
adding it again does nothing, says which task has the key and in what state, and
exits with 0. A cancelled task frees its key. `-force` adds the task even when its key
is taken, for a deliberate repeat (the key then belongs to both, and a later add
without `-force` is still a no-op).

```sh
$ echo '{"commit": "3f2a91c", "file": "notes.txt"}' | hetman task add -kind machine-finding -key lint:3f2a91c:notes.txt -input -
task 1 queued (kind machine-finding)
$ echo '{"commit": "3f2a91c", "file": "notes.txt"}' | hetman task add -kind machine-finding -key lint:3f2a91c:notes.txt -input -
task 1 already has the key "lint:3f2a91c:notes.txt" and is queued; nothing added (-force adds it again)
```

**Running them.** `hetman dispatch` takes queued tasks by route priority, then age,
and starts a campaign of the routed flow for each, with the task's input plus the
field `hetman_task` (`{"id": 1, "kind": "machine-finding", "key": "..."}`), which
prompt templates read as `{{input.hetman_task.kind}}` and scripts find in
`HETMAN_INPUT_FILE`. A task input may not have that field itself. It runs up to
`limit` campaigns at once (`-limit N` overrides the routing file) and at most a
route's own `limit` of that route, and keeps looking for new tasks every second
(`-poll`) until stopped; with `-once` it stops when nothing is left to start or
running. One dispatcher runs per folder; a second one is refused. The routing file is
read when the dispatcher starts; an orders file is read again before every step of a
campaign of it, as described under Concepts.

A campaign that waits for a person takes no place in the limits: the dispatcher lets
go of it, and once the person has answered (`hetman answer`, or the page) it continues
it in turn, before any new task. An answer from the page makes `hetman serve` drive the
campaign at once, as for any campaign; the dispatcher leaves a campaign another process
drives alone. A halted campaign, one stopped in the error state, or one waiting after a
subscription limit or lost connectivity is left to a person: whoever resumes it drives
it.

When the dispatcher is stopped (Ctrl+C) or killed, the campaigns it drives are cut off
as those of a stopped `hetman run`: the step that was running runs again, a finished
step never does. A dispatcher started again continues them first, without starting any
task a second time; a task whose campaign was being created at the moment of the kill
gets that campaign, or a new one if it was never created.

**Task states.** `hetman task list` and `hetman task show ID` show each task. A task is
`queued` or `cancelled` until it is started; then its state is that of its campaign,
read from the chronicle: `running` (also while the dispatcher is about to continue
it), `waiting` (for a person: an answer, or a resume after a limit or lost
connectivity; "waits for" says which), `halted`, `error`, `done` or `failed`. Only a
queued task can be cancelled (`hetman task cancel ID`); a started one is halted like
any campaign. `hetman task show` prints the campaign and the command that answers or
resumes it.

`examples/dispatch` routes two kinds: a machine finding fixed by a short loop with no
person (a script applies one fix, a gate checks the file, back until it is clean), and
a reviewer remark whose flow first asks a person what to do and then runs the same
loop. `collect.sh` adds three tasks, one a duplicate:

```sh
$ go build -o examples/dispatch/bin/stub-adapter ./cmd/hetman-stub-adapter
$ cd examples/dispatch
$ sh collect.sh
task 1 queued (kind machine-finding)
task 2 queued (kind reviewer-remark)
task 1 already has the key "lint:3f2a91c:notes.txt" and is queued; nothing added (-force adds it again)
$ hetman dispatch -once
...
task 1: campaign 1 done
task 2: campaign 2 waits for a person to decide at decide (fix, decline); answer with: hetman answer -f /path/to/examples/dispatch/remark.yaml -c 2
nothing left to start; 1 task(s) wait for a person (hetman task list)
$ hetman answer -f remark.yaml -c 2 -outcome fix -a how='drop the spaces'
$ hetman dispatch -once
task 2 (reviewer-remark): campaign 2 continues at decide
...
task 2: campaign 2 done
```

Watchtower shows the tasks of its folder under a Tasks tab next to the campaigns, with
kind, state, campaign, age and what a waiting task waits for. Campaign commands
(`status`, `halt`, `resume`, `answer`) given `-f` with another orders file of the same
folder use the orders file the campaign was started from.

## Interface language

Hetman's own messages (the command line, the wizard, campaigns in plain words and the
Watchtower page) come in the language of the person reading them: the one in
`HETMAN_LANGUAGE`, else English. The orders' `language` key does not change it: that
key is the flow's language, for what the flow addresses to a person, so the same
command speaks the same language for an orders file and for its segments, and setting
the key while building a flow does not switch the interface. Likewise the language a
campaign recorded is for what the flow produces, while the interface speaks to
whoever is looking. Any of the six codes can be chosen; a language without a translation
shows English. English and Ukrainian are translated now.

```sh
$ HETMAN_LANGUAGE=uk hetman halt
похід 3 стане на привал після поточного кроку
```

Watchtower starts in the language of the `hetman serve` process, chosen by the same
rule. A switcher in the page header offers all six languages and remembers the choice
in that browser; the plain-words tab follows it too.

What stays English in every language, because tools, agents and models read it: the
keys and values of the orders file; node, check and outcome names; the
`FILE:LINE:COL: message` problems of the orders check (and the `warning:` lines made
of them); chronicle event kinds and the `status -events` dump; log files; the adapter
protocol; everything sent to a model (prompts, the Scribe request, the language
instruction); environment variable names; the payload of the notification hook;
error messages that come from the operating system, a library, the orders editor or
the engine; the `FILE:LINE:COL` problems of the routing file; the state names
`hetman task list -state` takes (`queued`, `running`, `waiting`, ...), although the
STATE column shows them translated, as `hetman status` shows its states; and the error
bodies of the page's API. `hetman task` and `hetman dispatch` speak the language in
`HETMAN_LANGUAGE`, else English, since a routing file names several orders files. Durations printed by the command
line keep Go's form, such as `1.2s`.

**Translations.** The texts live in one file per language,
`internal/i18n/catalogue/CODE.yaml`, embedded in the binary. Each entry maps a key to a
message; names in braces, such as `{node}`, are filled in by the program. A message
that depends on a number has a form per plural category of the language, and the
number is always `{n}`:

```yaml
page.attempts:
  one: "{n} спроба"
  few: "{n} спроби"
  many: "{n} спроб"
```

The forms each language needs: Chinese `other`; English, Spanish and Hindi `one` and
`other`; Ukrainian `one`, `few` and `many`; Arabic `zero`, `one`, `two`, `few`, `many`
and `other`. `en.yaml` is the source and has every key. To fix a text, edit its
entry; to add a language, create its file with the keys translated so far: a missing
key shows the English text. The test of `scripts/verify.sh` fails on a key or a
`{name}` that English does not have and on a missing plural form, and lists the keys
each language has not translated yet; the Ukrainian file must be complete. Another
test reads the code that prints for a person and fails on English text written there
instead of in the catalogue.

## Setup assistant

Scribe, the setup assistant, builds and changes `orders.yaml` and tells what a
campaign did. It is deliberately simple: the program does the work, and a model, when
one is used at all, only picks one item from a short list. It has three layers, each
usable without the next:

1. **Operations**: a fixed set of commands that change the orders file.
2. **The wizard**: `hetman scribe` asks fixed questions and makes the operations. No
   model.
3. **A sentence**: `hetman scribe "add a gate with the lint check after build"` asks a
   model, named in the orders, which operation the sentence means; the program checks
   the answer, shows the change and applies it once you confirm.

It also explains campaigns in plain words, in the terminal and on the Watchtower page,
and it can write an instruction file for a coding agent that sets a flow up with the
same operations. Scribe never changes campaign state.

### Operations

```
hetman scribe new TYPE NODE [-flow NAME] [node flags]       start a new orders file with its first node
hetman scribe add TYPE NODE (-after NODE [-via OUTCOME] | -before NODE) [node flags]
hetman scribe remove NODE                    remove a node and connect around it
hetman scribe rename NODE NEW                rename a node wherever it is referenced
hetman scribe link NODE OUTCOME TARGET [-max N -then OUTCOME]   set or change a transition, limited or not
hetman scribe unlink NODE OUTCOME            remove a transition
hetman scribe set NODE FIELD VALUE           set a field of a node
hetman scribe unset NODE FIELD               remove a field of a node
hetman scribe check NAME [-run CMD] [-timeout D] [-soft | -required] [-locks A,B]   declare a check, or change one
hetman scribe executor NAME -run CMD         declare an executor, or change its command
hetman scribe gate-add GATE CHECK [-at N]    put a declared check into a gate, at position N from 1
hetman scribe gate-remove GATE CHECK         take a check out of a gate
hetman scribe ask NODE ID QUESTION [-kind yesno|choice|text] [-options A,B]
                                             add a question to a gate or a human node
hetman scribe role NAME [-select MODE] [-executor X [-model M] [-effort E]]
                                             declare a role with its mode and first client, or change its mode
hetman scribe role-add ROLE EXECUTOR [-model M] [-effort E] [-at N]   add a client to a role
hetman scribe role-remove ROLE N             remove the client at position N from 1; a role keeps one
hetman scribe assistant EXECUTOR MODEL [-effort E]   name the model Scribe may ask
hetman scribe language CODE                  set the language for the person
hetman scribe unlanguage                     remove it: English, and nothing added to prompts
hetman scribe workdir FOLDER                 set the folder agent steps work in by default
```

`TYPE` is `script`, `gate`, `agent`, `human`, `segment` or `foreach`. The node flags
are `-run CMD` (one string split at spaces; quotes keep spaces in an argument),
`-timeout D`, `-checks A,B`, `-executor NAME`, `-role NAME` (instead of `-executor`,
`-model` and `-effort`), `-model M`, `-effort E`, `-tools A,B`, `-attempts N`,
`-prompt FILE`, `-schema FILE`, `-outcomes A,B` (of an agent or human node),
`-workdir FOLDER` (of an agent node; for `new`, the default of the file),
`-on OUTCOME=TARGET` (repeatable), `-starters`, `-locks A,B` (any node),
`-segment FILE` (segment and foreach), `-input REF` (segment), `-items REF` and
`-parallel N` (foreach). A list flag, and a list given as the value of `set`, ends an
entry only at a comma or a space outside parentheses and quotes, so
`-tools 'Read,Edit,Write,Grep,Glob,Bash(go test *)'` gives six entries, the last one
whole; quotes keep an entry with a comma together and are dropped. The fields of
`set` are `run`, `timeout`, `idle_timeout`, `executor`, `role`, `model`, `effort`,
`tools`, `attempts`, `dialogue`, `prompt`, `schema`, `workdir`, `fail_on_denied`
(`true` or `false`), `locks`, `segment`,
`input`, `items` and `parallel`; `unset` takes the optional ones of these. For
example, `hetman scribe add foreach build-all -after plan -segment segments/item.yaml
-items output.plan.items -parallel 2` and `hetman scribe set test locks test-stand`.
`hetman scribe help` lists everything.

How the flow is wired:

- A node's main outcome is `ok`, else `pass`, else its first outcome that is not
  `fail`, `timeout` or `blocked`.
- `add -after X` makes X's main outcome (or `-via OUTCOME`) lead to the new node; the
  new node's main outcome leads where X's did. `add -before Y` makes the start, if it
  was Y, and every main outcome that led to Y lead to the new node, whose main outcome
  leads to Y; loops back to Y stay as they are. Every other outcome of a new node
  leads to `failed` unless `-on` says otherwise.
- `remove` makes every transition that led to the node lead where its main outcome
  led, and moves the start the same way.
- `rename` changes the node's key, the start, every transition and every placeholder
  that names it in a prompt template (`{{output.NODE}}`, `{{artifact.NODE...}}`,
  `{{answers.NODE...}}`).
- `set NODE timeout` on a script, or a gate with `run`, adds `timeout: failed` when
  the node has no transition for `timeout`; `unset NODE timeout` removes it.
- `link NODE OUTCOME TARGET -max N -then OTHER` writes a limited transition
  `{to: TARGET, max: N, then: OTHER}` and adds `OTHER: failed` when OTHER has no
  transition yet; `link` without them writes a plain one, removing a limit.
  `rename` and `remove` keep the limit of a transition they redirect.
- `new` writes `workdir: .` at the top (or the folder `-workdir` names), so a flow
  started with any node can take agent steps later.
- `set NODE role R` removes the node's `executor`, `model` and `effort`, since each
  client of the role names its own; `set NODE executor X` on a node run through a role
  removes `role`. A client's `allowance` is written by hand.
- For an agent node, `-prompt` and `-schema` default to `prompts/NODE.md` and
  `schemas/NODE.json`. An existing schema gives the outcomes. `-starters` creates a
  minimal template and schema where those files do not exist; an existing file is
  never written over.

Every operation, from the command line or not, works the same way:

- It edits the file in place. Everything outside the lines it changes stays byte for
  byte as it was: comments, blank lines, key order, quoting, alignment, line ends
  (CR LF stays CR LF). New entries take the style of their neighbours: a node written
  as a block gets a block, a node on one line gets one line, transitions in braces get
  braces. A comment directly above a removed node goes with it; a comment at the end
  of a changed line stays. New lines are not aligned with their neighbours.
- If an entry is written in a form that cannot be edited in place (a multi-line
  string, an anchor, a block list changed into another), the whole file is written
  out by the YAML library instead and a note says so. That keeps the comments and the
  key order, but not blank lines, alignment or quoting.
- It runs the orders check on the result. If the result would not pass, nothing is
  written, the problems are printed and the command exits with 2. A program in a
  `run` list that does not exist yet is only a warning, since the script may be
  written after the step that runs it; `hetman check` still requires it. So is a
  prompt placeholder that names a node the orders do not have yet, such as
  `{{artifact.prove.self-test.log}}` before `prove` is added (`there is no node
  "prove" in the orders (yet)`), so a flow can be built front to back without
  `-force`; `hetman check` still rejects it.
- It prints the change as a unified diff. `-dry-run` prints it and writes nothing.
- `-force` writes a result that does not pass the check. It is for a state meant to
  be finished by the next operation: a gate that only asks a person is added with
  `-force` and then given its question with `ask`; a flow whose first step is an
  agent step is started with `-force` and then given its executor.

The operations are one Go package, `internal/edit`, which the wizard, the sentence
layer and later a page editor all call.

### The wizard

`hetman scribe` with no arguments asks fixed questions in the terminal and offers
suggestions taken from the current file (the step to add after, the declared checks
and executors). On a missing file it starts a new flow. A new flow also gets the language for
the person, picked by number, by code or by typing its name (`ukrainian`, `українська`);
Enter keeps English. Each change is shown as a
diff and written only when confirmed. A session that builds a flow from nothing
(answers after the colons; an empty answer takes the suggestion in brackets):

```
$ mkdir shop && cd shop && hetman scribe
Scribe wizard for orders.yaml. No model is used.
Enter takes the suggestion in [brackets]; a number picks from a list.

orders.yaml does not exist yet, so this starts a new flow.
Flow name [shop]:
Which language should the flow use for what it addresses to a person (questions, summaries, plans)?
Code, code comments and everything written into the repository stay in English.
  0) skip, keep English
  1) ar  Arabic (العربية)
  2) en  English (English)
  3) es  Spanish (español)
  4) hi  Hindi (हिन्दी)
  5) uk  Ukrainian (українська)
  6) zh  Mandarin Chinese (普通话)
Language: a number, a code or a name [skip]:
The first step of the flow.
What kind of step?
  1) script (runs a command)
  2) agent (a model does the work)
  3) human (a person decides)
Choice [script (runs a command)]: 1
Step name (letters, digits, - _ .): prepare
Command to run (program and arguments) [sh steps/prepare.sh]:
Time limit, such as 5m (Enter for none):
If it fails, go to
  1) prepare
  2) done
  3) failed
Choice [failed]:

The change:
  - create a new flow named shop whose first step is the script node prepare, running sh steps/prepare.sh
--- /dev/null
+++ orders.yaml
@@ -0,0 +1,9 @@
+version: 0
+flow: shop
+start: prepare
+
+nodes:
+  prepare:
+    type: script
+    run: [sh, steps/prepare.sh]
+    on: {ok: done, fail: failed}
Write it? (y/n) [y]:
Wrote orders.yaml.

What next?
  1) add a step
  2) add a gate
  ...
  11) quit
Choice [quit]: 1
...                                   (build after prepare with a limit of 5m, then
...                                    test after build, whose failure goes back to build)
Choice [quit]: 2
Gate name [gate]: ready
Where does it go?
  1) after a step
  2) before a step
Choice [after a step]:
After which step?
  1) prepare
  2) build
  3) test
Choice [test]:
Declare the checks the gate runs, cheapest first.
Check name: lint
Command to run (program and arguments) [sh checks/lint.sh]:
If it fails, should the gate block or only warn?
  1) required (blocks the gate)
  2) soft (only warns)
Choice [required (blocks the gate)]: soft
Time limit, such as 1m (Enter for none):
Declare another check for this gate? (y/n) [n]: y
Check name: tests
Command to run (program and arguments) [sh checks/tests.sh]:
If it fails, should the gate block or only warn?
  1) required (blocks the gate)
  2) soft (only warns)
Choice [required (blocks the gate)]:
Time limit, such as 1m (Enter for none): 1m
Declare another check for this gate? (y/n) [n]:
If the gate blocks, go to
  ...
Choice [failed]:

The change:
  - declare or change the check lint, running sh checks/lint.sh, soft (it only warns)
  - declare or change the check tests, running sh checks/tests.sh, with the timeout 1m, required (it blocks the gate)
  - add the gate node ready after test, with the checks lint, tests
--- orders.yaml
+++ orders.yaml
@@ -3,4 +3,8 @@
 start: prepare
 
+checks:
+  lint: {run: [sh, checks/lint.sh], soft: true}
+  tests: {run: [sh, checks/tests.sh], timeout: 1m}
+
 nodes:
   prepare:
@@ -16,3 +20,7 @@
     type: script
     run: [sh, steps/test.sh]
-    on: {ok: done, fail: build}
+    on: {ok: ready, fail: build}
+  ready:
+    type: gate
+    checks: [lint, tests]
+    on: {pass: done, blocked: failed}
Write it? (y/n) [y]:
Wrote orders.yaml.
...                                   (publish after ready)
Choice [quit]:
```

Then two operations change it: a blocked gate sends the flow back to `build`, and the
gate gets a better name.

```
$ hetman scribe link ready blocked build
Make the outcome blocked of ready lead to build.
--- orders.yaml
+++ orders.yaml
@@ -24,5 +24,5 @@
     type: gate
     checks: [lint, tests]
-    on: {pass: publish, blocked: failed}
+    on: {pass: publish, blocked: build}
   publish:
     type: script
Wrote /path/to/shop/orders.yaml.
$ hetman scribe rename ready checked
Rename the node ready to checked everywhere it is referenced.
--- orders.yaml
+++ orders.yaml
@@ -20,6 +20,6 @@
     type: script
     run: [sh, steps/test.sh]
-    on: {ok: ready, fail: build}
-  ready:
+    on: {ok: checked, fail: build}
+  checked:
     type: gate
     checks: [lint, tests]
Wrote /path/to/shop/orders.yaml.
```

When the input is not a terminal, the wizard writes every answer it reads after its
question, so that a scripted session reads like a typed one.

### A sentence, through a model

A sentence works only when the orders name the executor and model Scribe may use;
there is no default:

```yaml
scribe: {executor: stub, model: any-model}   # also effort, and timeout (5m when not given)
```

`hetman scribe assistant EXECUTOR MODEL` writes it. Without it, `hetman scribe
"..."` says so and points to the wizard and the operations.

For a sentence Hetman sends one request through the adapter protocol, as for an agent
step, with run and node id `scribe`, no tools and no session. The prompt holds the
sentence, a compact description of the flow (each node with its type, checks,
executor and model, questions, timeout and transitions; the declared checks and
executors; the campaign numbers), the list of operations with their fields, and the
query kinds below. The model never sees the text of the file. The schema of the
answer allows exactly one of:

- `{"outcome": "operation", "operation": {"op": "add", "type": "gate", "node": "lint_gate", "after": "build", "checks": ["lint"]}}`
- `{"outcome": "query", "query": {"kind": "rework", "campaign": 3, "node": "build"}}`
- `{"outcome": "unclear", "choices": [{"question": "Do you mean ...?", "operation": {...}}, ...]}`, at most three

The program checks the answer against the schema and the rules of the operation. An
answer that does not fit is asked for once more, with the reason; a second one that
does not fit ends it. An operation that names something the file does not have, or
whose result would not pass the orders check, is refused. In all those cases nothing
is written. An unclear answer lists its interpretations and you pick one, or none;
nothing is guessed. A clear operation is shown in plain words with its diff and
applied only after you answer `y`; `-yes` applies it without asking (an unclear
answer still changes nothing). A query is answered by the explanation below; the
model writes none of the answer. The prompt, log and diagnostics of the last request
are in `.hetman/scribe/`.

```
$ hetman scribe "add an agent step named notes before publish, run by stub"
Scribe proposes: add the agent node notes before publish, through the executor stub.
--- orders.yaml
+++ orders.yaml
@@ -29,5 +29,11 @@
     type: gate
     checks: [lint, tests]
-    on: {pass: publish, blocked: build}
+    on: {pass: notes, blocked: build}
+  notes:
+    type: agent
+    executor: stub
+    prompt: prompts/notes.md
+    schema: schemas/notes.json
+    on: {ok: publish, fail: failed}
   publish:
     type: script
--- /dev/null
+++ prompts/notes.md
...
Apply this change? [y/N] y
Wrote /path/to/shop/orders.yaml.

$ hetman scribe "make the checks stricter"
The sentence can mean more than one thing. Did you mean:
  1) Should lint block the gate instead of only warning?
     (declare or change the check lint, required (it blocks the gate))
  2) Should tests get less time, 30s instead of 1m?
     (declare or change the check tests, with the timeout 30s)
  0) none of these
Choice [0-2]: 0
Nothing was changed.
```

The stub adapter (`cmd/hetman-stub-adapter`) can answer by the text of the prompt
(`"when"` in its plan), so all of this can be tried without a model.

### Campaigns in plain words

```
hetman scribe explain [-f FILE] [-c ID] [-kind KIND] [-node NODE]
```

tells a campaign in sentences built by fixed templates from its chronicle events
alone: where it is, what it waits for and since when, what ran with what outcome, why
the flow went back and how many times, what failed with the recorded reason, and which
soft checks warned. The kinds are `where`, `history`, `rework`, `failures`,
`warnings` and `summary` (all of them, the default); `-node` keeps the parts about
one node. It never states anything the chronicle does not hold.

```
$ hetman scribe explain -c 2
Where it is:
  Campaign 2 of flow shop started at 2026-11-14 23:39:11 at prepare.
  It has been stopped at notes since 2026-11-14 23:40:24: attempt 1 lost connectivity (network is unreachable). The attempt does not count; the step runs again when the campaign is resumed.

What ran:
  2026-11-14 23:39:11  prepare attempt 1 finished with ok after 0s. The flow went to build.
  ...
  2026-11-14 23:39:11  checked asked a person: "Are the release notes written?".
  2026-11-14 23:40:23  dana answered checked from the command line: notes: yes.
  2026-11-14 23:40:24  checked attempt 2 finished with pass after 1m13s; checks: lint fail (soft, a warning), tests ok. The flow went to notes.
  2026-11-14 23:40:24  notes attempt 1 was cut off (offline): network is unreachable; it does not count.

Rework:
  The flow went back to build once.
  2026-11-14 23:39:11  checked finished with blocked and sent the flow back to build (required check tests reported fail).

What failed:
  2026-11-14 23:39:11  checked attempt 1 finished with blocked: required check tests reported fail.
  2026-11-14 23:40:24  notes attempt 1 was cut off (offline): network is unreachable; it does not count.

Warnings:
  2026-11-14 23:39:11  The soft check lint of checked reported fail in attempt 1; it only warned.
  2026-11-14 23:39:11  The soft check lint of checked reported fail in attempt 2; it only warned.
```

A question to the model such as `hetman scribe "why did campaign 3 go back to build
twice?"` is mapped to a kind, a campaign and a node, and answered by the same
templates. Watchtower shows the same explanation on the "In words" tab of a campaign.

### The agent skill

`hetman scribe skill` writes `SKILL.md`, an instruction file for a coding agent, into
`.agents/skills/hetman-scribe` (another folder with `-dir`). It interviews a person
about their project and builds the flow with the operations above, editing by hand
only what no operation covers (prompt templates, schemas, `show`, `notify`). Running it
again prints `PATH is up to date` when the file is the same; a different file is left
alone unless `-force` is given.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
