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
flow goes next.

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

**Nodes.** There are three types.

- `script` runs one command. Exit code 0 gives the outcome `ok`, anything else
  `fail`.
- `gate` decides whether the flow may go on, with the outcomes `pass` and
  `blocked`. It has either a single `run` command (exit code 0 gives `pass`) or a
  list of `checks`, never both.
- `agent` gives one step to a coding agent through an executor adapter. Its outcomes
  are the values its output schema allows for `outcome`, plus `fail`. See Agent
  steps below.

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
campaign. `hetman run -input FILE` gives it a run input: a JSON object that prompt
templates read. It is stored in the chronicle with the campaign, so a resumed
campaign sees the same input.

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
```

Add `.hetman/` to your version control ignore list.

**Halt and resume.** `hetman halt` asks a campaign to stop after its current step;
if no process is running it, it halts at once. `hetman resume` continues a halted
campaign, a campaign interrupted in the middle of a step (that step runs again), or
a campaign stopped in the error state. A campaign stops in the error state, and
waits for a person, when a program cannot be started, a result file is not valid
JSON, an outcome has no transition, the current node is no longer in the orders, an
adapter does not describe itself properly, or a prompt template reads a run input
field that does not exist. Fix the cause and resume. The orders file is read again
on every resume.

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
    model: "..."                     # optional, passed to the adapter unchanged
    effort: "..."                    # optional, passed to the adapter unchanged
    tools: [read, edit]              # optional, passed to the adapter unchanged
    timeout: 30m                     # optional: limit for one attempt
    idle_timeout: 5m                 # optional: limit for silence from the adapter
    max_attempts: 3                  # optional, 1 to 100, default 3
    on: {ok: review, fail: failed}
```

**Executors.** `executors` maps a name to the program of an adapter, an argv list
like `run` of a script. The adapter is what talks to a client; Hetman never knows
which one. `hetman check` looks for the adapter program as it does for scripts.

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
| `{{artifact.NODE.FILE}}` | the text of FILE in the work folder, when script or gate NODE listed it in its result file |
| `{{rework}}` | empty on the first visit of the node; when the flow came back to it, the step that sent it back, its outcome and its answer, failed checks or artifacts |

A text value is inserted as it is, any other value as indented JSON. A node id may
contain dots; the longest node id that fits is used. A run input field that does not
exist stops the campaign in the error state; an answer or artifact that does not
exist yet gives empty text. `hetman check` reports an unknown placeholder, an
`output` of a node that is not an agent, and a field the node's schema rules out,
at its line in the template. `{{` not followed by a name and `}}` stays as text. The
prompt of every attempt is stored under `prompts/` in the campaign folder.

**Attempts.** Each node visit has `max_attempts` attempts. An attempt fails when the
answer does not match the schema, the adapter reports a transient error, the adapter
exits without a proper answer or breaks the protocol, the attempt runs past
`timeout`, or the adapter sends no message for `idle_timeout`; it is then run again.
After a rejected answer, the next prompt ends with what was wrong with it. When the
attempts are used up, the node's outcome is `fail`. A permanent error gives `fail`
at once. A node the flow comes back to starts with all its attempts again.

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

## Adapters

An adapter is the program between Hetman and one coding-agent client: it receives
one step, runs the client on it, and reports what the client does and answers, in
the protocol described under Writing an adapter. The `hetman` binary contains no
adapter and never names a client; an orders file names the adapter program of each
executor. This repository has two: `hetman-adapter-claude` for the Claude Code
client, and `hetman-stub-adapter`, which runs no model (see Agent steps without a
real client).

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

For each attempt it runs the client in the orders folder, with the prompt on
standard input:

```
claude -p --output-format stream-json --verbose --restricted --strict-mcp-config
  --permission-mode dontAsk --permission-prompts none
  --tools=Read,Edit,Write,Grep,Glob,Bash --allowedTools=Read ... --allowedTools=Bash(go test *)
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
  and its file tools stay inside the orders folder.
- The answer is the client's structured output, checked by the client against the
  node's schema and again by Hetman. When the client gives only text, the last JSON
  object in it is taken; no object at all is a transient error.
- Errors: a subscription limit is `limit`, with the reset time when the client gives
  one; a failed connection is `offline`; not logged in, an unknown model and bad
  arguments are `permanent`; anything else is `transient`.
- The log shows what the client does: its text, each tool call and what it was
  called on, and the first line of each tool result.
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

`examples/claude` is a small flow with this adapter: a draft step that writes a note
into a file, a gate that checks the file, and a review step that may send the draft
back once. Its comments say how to run it; a run makes two to four small requests
with the `haiku` model.

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

`protocol` lists the versions it speaks and must include 0. The only feature so far
is `resume`.

**Start.** Hetman then starts the program without `--describe` and writes:

```json
{"v":0,"type":"start","step":{"run_id":"1","node_id":"implement","attempt":1,
 "prompt":"...","model":"...","effort":"...","tools":["read","edit"],
 "workdir":"/path/to/project","schema":{...},"session":null}}
```

`model`, `effort` and `tools` are the strings from the orders (empty when not set).
`schema` is the node's schema. `session` is null, or the id to continue.

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

It sends three tiny steps and one that is cancelled; `HETMAN_ADAPTER_MODEL` and
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
hetman serve  [-f FILE] [-addr ADDRESS] show every campaign live on a web page (Watchtower)
hetman scribe [-dir DIR] [-force]       write the setup assistant skill for a coding agent
hetman version                          print the version of hetman
```

- `-f FILE` is the orders file, `orders.yaml` in the current folder by default.
- `-input FILE` is a JSON file holding one object, the run input of the campaign.
- `-c ID` is the campaign number; the latest campaign by default.
- `-events` makes `status` also list every chronicle event.
- `-addr ADDRESS` is where `serve` listens, `127.0.0.1:8740` by default.
- `-dir DIR` is the folder `scribe` writes `SKILL.md` into,
  `.agents/skills/hetman-scribe` in the current folder by default. `-force` replaces
  a different `SKILL.md` that is already there.

`scribe` and `version` need no orders file.

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
| 5 | an agent step hit a subscription limit with no time to retry, or lost connectivity; waiting for a person |
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
artifacts, a timeline, and the chronicle events. For an agent step the card shows
the executor, model and effort, the prompt of each attempt, the adapter's log live
while it runs, its diagnostics, the accepted answer, the session id, and why a
failed attempt failed. A campaign waiting for a subscription limit, or stopped by
one or by lost connectivity, is marked as such. Everything on it comes from the
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
