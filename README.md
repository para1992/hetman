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
  the engine ignores it.
- Ids and names use letters, digits, `_`, `-` and `.`.
- `run` is a list: the program followed by its arguments. There is no shell. A
  program with a slash in it is relative to the orders folder; a bare name such as
  `make` is looked up on the path. On Windows a program must be one Windows can
  start (`.exe`, `.cmd`, `.bat`); run a script through its interpreter, as in
  `[sh, steps/build.sh]` or `[python3, steps/build.py]`.
- `timeout` is a duration such as `30s`, `10m` or `1h30m`. When it runs out, the
  command and every process it started are killed.

**Nodes.** There are four types.

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
| `{{answers.NODE}}` | the latest answers a person gave at gate or human node NODE: each question, answer and comment, and the chosen outcome |
| `{{answers.NODE.ID}}` | the answer to question ID there (`yes`, `no`, an option or the text) |
| `{{answers.NODE.ID.comment}}` | the comment on that answer |
| `{{language}}` | the English name of the campaign's language for the person (`English` when none is set) |
| `{{language.native}}` | its own name, in its own script |
| `{{language.code}}` | its code, such as `hi` |
| `{{rework}}` | empty on the first visit of the node; when the flow came back to it, the step that sent it back, its outcome and its answer, failed checks or artifacts, and what a person answered there |

A text value is inserted as it is, any other value as indented JSON. A node id may
contain dots; the longest node id that fits is used. A run input field that does not
exist stops the campaign in the error state; an answer or artifact that does not
exist yet gives empty text. `hetman check` reports an unknown placeholder, an
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
    show: [plan.md]           # files the person looks at before answering
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
the terminal) or can read (on the page) before answering.

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
is the number to pass as `-ask`. The campaign never waits for the hook: it runs in the
background and is killed after `timeout` (30 seconds by default). Its output goes to
`logs/notify.EVENT.log` in the campaign folder and how it ended to a `notified` event.
A process that ends waits for hooks it started, up to their timeout. Any messenger can
be connected with a small script that sends the message and, when the person replies,
runs the `hetman answer` command with flags.

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

For each attempt it runs the client in the orders folder, with the prompt on
standard input:

```
opencode run --format json --pure --print-logs --log-level INFO --agent hetman-XXXXXXXX
  --model PROVIDER/MODEL --variant EFFORT (--session SESSION | --title TITLE)
```

and gives it, in `OPENCODE_CONFIG_CONTENT`, an agent made for this run only whose
permissions deny everything and then allow the node's tools.

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
  option that skips permission checks (`--auto`), and its file tools stay inside the
  orders folder. `task` is refused, because the subagents it starts run with the
  client's default permissions. External plugins are not loaded (`--pure`). The
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
- The log shows what the client does: its text, each tool call and what it was
  called on, the first line of each tool result or error, and the model steps and
  tokens at the end.
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

`protocol` lists the versions it speaks and must include 0. The only feature so far
is `resume`, which dialogue steps need.

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

It sends tiny steps: one, one resumed and a dialogue of two steps (both only for an
adapter that declares `resume`), and one that is cancelled; `HETMAN_ADAPTER_MODEL` and
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
hetman answer [-f FILE] [-c ID] [-a ID=ANSWER ...] [-comment ID=TEXT ...] [-outcome NAME] [-ask N] [-by NAME]
                                        answer what a campaign asks a person
hetman serve  [-f FILE] [-addr ADDRESS] show every campaign live on a web page (Watchtower)
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
- `-addr ADDRESS` is where `serve` listens, `127.0.0.1:8740` by default.
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
| 5 | an agent step hit a subscription limit with no time to retry, or lost connectivity; waiting for a person |

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

It has two controls, Halt and Resume, which do what `hetman halt` and
`hetman resume` do, and an answer form. A campaign resumed from the page is run by the
`hetman serve` process; stopping that process interrupts it, and `hetman resume`
continues it.

The card of a step that asks shows the files to look at, the questions, and, while
they are open, a form: yes or no and a comment for each question of a gate; the
questions of a human node and the choice of its outcome; the questions of an agent
step in dialogue. It also shows the `hetman answer` commands that do the same.
Submitting the form goes through the same checks as `hetman answer`: answers that do
not fit are shown in the form and change nothing. Recorded answers are continued by
the process that drives the campaign, or by `hetman serve` when none does. Once
answered, the card shows the answers, who gave them and when.

## Interface language

Hetman's own messages (the command line, the wizard, campaigns in plain words and the
Watchtower page) come in the language of the person reading them: the one in
`HETMAN_LANGUAGE`, else the orders' `language` key, else English. For a command about
a campaign the person's language still wins over the language the campaign recorded:
the interface speaks to whoever is looking, while the campaign's language is for what
the flow produces. Any of the six codes can be chosen; a language without a translation
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
the engine; and the error bodies of the page's API. Durations printed by the command
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
hetman scribe link NODE OUTCOME TARGET       set or change a transition
hetman scribe unlink NODE OUTCOME            remove a transition
hetman scribe set NODE FIELD VALUE           set a field of a node
hetman scribe unset NODE FIELD               remove a field of a node
hetman scribe check NAME [-run CMD] [-timeout D] [-soft | -required]   declare a check, or change one
hetman scribe executor NAME -run CMD         declare an executor, or change its command
hetman scribe gate-add GATE CHECK [-at N]    put a declared check into a gate, at position N from 1
hetman scribe gate-remove GATE CHECK         take a check out of a gate
hetman scribe ask NODE ID QUESTION [-kind yesno|choice|text] [-options A,B]
                                             add a question to a gate or a human node
hetman scribe assistant EXECUTOR MODEL [-effort E]   name the model Scribe may ask
hetman scribe language CODE                  set the language for the person
hetman scribe unlanguage                     remove it: English, and nothing added to prompts
```

`TYPE` is `script`, `gate`, `agent` or `human`. The node flags are `-run CMD` (one
string split at spaces; quotes keep spaces in an argument), `-timeout D`,
`-checks A,B`, `-executor NAME`, `-model M`, `-effort E`, `-tools A,B`, `-attempts N`,
`-prompt FILE`, `-schema FILE`, `-outcomes A,B` (of an agent or human node),
`-on OUTCOME=TARGET` (repeatable) and `-starters`. The fields of `set` are `run`,
`timeout`, `idle_timeout`, `executor`, `model`, `effort`, `tools`, `attempts`,
`dialogue`, `prompt` and `schema`; `unset` takes the optional ones of these.
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
  written after the step that runs it; `hetman check` still requires it.
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
