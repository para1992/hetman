---
name: hetman-scribe
description: Set up a Hetman flow for a project through a short interview. Use when someone wants to describe their build, check, release or agent-driven process as a Hetman orders.yaml, or to change an existing one. Asks plain questions one at a time, builds orders.yaml with the hetman scribe operations (which edit the file, keep its comments and refuse a result that does not pass the check), writes prompt templates, output schemas and any small check scripts the person asks for, and runs hetman check until the file is accepted.
---

# Setting up a Hetman flow

You are helping someone describe a process (build, test, publish, render, anything
made of commands, and steps done by a coding agent) as a Hetman flow. Hetman runs the flow step by step from a config
file called `orders.yaml`, records every step in a journal (the chronicle) and decides
on its own which step comes next.

Your job is to interview the person, build `orders.yaml` with the `hetman scribe`
operations (see "Changing the file: use the operations"), write the prompt templates
and output schemas of any agent steps and small check scripts if they ask for them,
and run `hetman check` until the file is accepted. The file you
write is the product: a plain file the person can read, review in version control and
edit by hand later. Write it so that it reads well without you: short comments, step
names that say what the step does, no clever tricks.

The person may not be a programmer. Ask one thing at a time, in plain words, and
explain a term the first time you use it.

## What Hetman can express

This is everything the config supports. If the person wants something that is not
here (a retry limit on a script, a person answering on a fixed schedule,
conditions in the file or in a prompt template),
say plainly that Hetman does not do that, and offer the
nearest thing that works: usually a script that does the extra logic itself. Do not
invent keys; `hetman check` rejects any key it does not know.

### The file

```yaml
version: 0            # required, always 0
flow: site            # optional name; defaults to the file name without .yaml
start: build          # required: the node the flow begins with
language: es          # optional: the language of what the flow addresses to a person

checks:               # optional: checks that gates can use, declared once
  style: {run: [./checks/style.sh], soft: true}
  tests: {run: [./checks/tests.sh], timeout: 10m}

executors:            # optional: adapter programs that run agent steps, declared once
  coder: {run: [./adapters/my-adapter]}
  other: {run: [./adapters/other-adapter]}

roles:                # optional: pools of clients an agent step may run on (see Agent steps)
  writer:
    select: round_robin
    clients: [{executor: coder, model: "..."}, {executor: other, model: "..."}]

notify: {run: [./notify.sh], timeout: 30s}   # optional: run when a campaign waits for a person
scribe: {executor: coder, model: "..."}      # optional: the model hetman scribe may ask (see below)

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

There are six node types.

- `script` runs one command. Its outcome is `ok` when the command exits with code 0
  and `fail` otherwise.
- `gate` decides whether the flow may go on. Its outcomes are `pass` and `blocked`.
  A gate has exactly one of these two forms:
  - `run: [...]`: a single command; exit code 0 gives `pass`, anything else `blocked`.
  - `checks: [a, b, c]`: names of checks declared under the top-level `checks`. The
    engine runs them in the listed order. The gate passes when every required check
    passed. A failed required check blocks the gate at once and the checks after it
    are not run. A failed soft check is recorded as a warning and the gate goes on.

  A gate may also ask a person yes/no questions (`ask`, see "People in the flow"), with
  or without `checks`, but not with `run`.

- `agent` gives one step to a coding agent through an executor adapter (see "Agent
  steps" below). Its outcomes are the values its output schema allows for
  `outcome`, plus `fail`.
- `human` waits for a person, who chooses one of the outcomes in its `on` (see
  "People in the flow").
- `segment` runs another orders file to its end and `foreach` runs one once per item
  of a list, several at a time (see "Segments, for-each and locks").

Any node may have `locks: [name, ...]`, resources its steps hold exclusively.

A check is `{run: [...]}` with optional `timeout`, optional `soft: true` (checks are
required unless marked soft) and optional `locks`. A check passes only when its outcome is `ok`; a failure
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
| `gate` with `checks` or `ask` | `pass`, `blocked` (nothing else is allowed in its `on`) |
| `agent` | every value of `outcome` in its schema, plus `fail` (nothing else is allowed in its `on`) |
| `human` | the keys of its `on`, whatever you name them; the person chooses one |
| `segment`, `foreach` | `done`, `failed` (nothing else is allowed in its `on`) |

If a step produces an outcome that has no transition, the campaign stops in an error
state and waits for a person.

A transition back can be limited: `blocked: {to: fix, max: 3, then: stuck}` is taken
at most 3 times per campaign; after that the outcome `stuck` is taken instead, which
needs its own transition in the same `on` (`stuck: failed`, or a human node), even on
a gate. Use it instead of a helper script that counts rounds. The count comes from
the chronicle; `hetman retry` keeps it and `hetman rewind` to a node before the loop
resets it.

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
| `HETMAN_INPUT_FILE` | Path of a JSON file holding the run input (`{}` when there is none) |
| `HETMAN_LANGUAGE` | Code of the campaign's language for the person; not set when the flow has none |

Steps hand files to later steps through `HETMAN_RUN_DIR`. Hetman keeps two kinds
there itself: `outputs/NODE.json`, the accepted output of agent node NODE, and
`answers/NODE.json`, what a person answered at NODE. Because every command can read
the run input, one generic check can serve every task: for example a check that
reads the task id from `HETMAN_INPUT_FILE` and runs that task's acceptance script. Output printed by a step is
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

### Agent steps

An agent node hands one step to a coding agent. The agent only does the step; Hetman
decides what comes next from the outcome the agent reports and the transitions in the
file.

```yaml
executors:
  coder: {run: [./adapters/my-adapter]}   # the adapter program and its arguments

nodes:
  implement:
    type: agent
    executor: coder                  # required: a name under executors
    prompt: prompts/implement.md     # required: the prompt template
    schema: schemas/implement.json   # required: the JSON schema of the answer
    workdir: ..                      # required here or at the top level: where the agent works
    model: "..."                     # optional, passed to the adapter as is
    effort: "..."                    # optional, passed to the adapter as is
    tools: [read, edit]              # optional, passed to the adapter as is
    timeout: 30m                     # optional: limit for one attempt
    idle_timeout: 5m                 # optional: limit for silence from the adapter
    max_attempts: 3                  # optional, 1 to 100, default 3
    on: {ok: review, fail: failed}
```

- **Executor.** An adapter is a separate program that drives one client. Hetman does
  not know or care which client it is. Ask the person which adapter program they use
  and how it is started; do not write an adapter yourself. `model`, `effort` and
  `tools` mean whatever that adapter says they mean; copy the person's values, never
  invent them.
- **Working folder.** `workdir` is the folder the agent works in, relative to the
  orders file: `.` for the folder of the orders, `..` when the orders live in a
  subfolder of the project (`flow/orders.yaml`) and the agent must change the code
  around it. Set it once at the top level (`workdir: ..`) or on each agent node;
  there is no default. Hetman keeps every agent out of `.hetman/` and the files of the
  orders (prompts, schemas, scripts) whatever the folder, except the campaign's work
  folder, which stays writable; never point `workdir` into `.hetman/`. A shell command
  the agent may run is not restricted by this, so give `Bash`-like tools only as
  narrow patterns.
- **Tools.** Copy the entries the person gives, one per item, quoted when they hold
  spaces (`"Bash(go test *)"`). Parentheses must pair up. Which forms a client honours
  is for its adapter; `hetman check` asks an adapter that can check, and reports what
  it rejects at its line, for example an entry such as `Write(plan.md)` that a client
  matches only when written `Edit(plan.md)`.
- **Schema.** A JSON file in a small subset of JSON Schema: `type` (object, array,
  string, number, integer, boolean, null), `properties`, `required`, `enum`, `items`,
  `additionalProperties`, plus `title` and `description`, which are ignored. Nothing
  else is accepted. The top level must be an object with a required string property
  `outcome` whose `enum` lists the outcomes the step may report. Each of them needs a
  transition in `on`, and `on` may not name an outcome the enum leaves out (except
  `fail` and `cannot_proceed`). Add the other fields the next steps need, for example
  `notes` for a review. Do not list `cannot_proceed` in the enum or add a property
  `cannot_proceed_reason`: Hetman adds them itself, so an agent that cannot do its
  step (a denied tool, say) can say why. Without a transition for `cannot_proceed` the
  campaign then stops and waits for a person with the reason; with one
  (`cannot_proceed: ask_lead`) the flow follows it.
- **Prompt template.** Plain text with placeholders in double braces, nothing else (no
  conditions, no loops):

  | Placeholder | Becomes |
  |---|---|
  | `{{input}}` | the whole run input, as JSON |
  | `{{input.FIELD}}` | a field of the run input (dots go deeper: `{{input.ticket.title}}`) |
  | `{{output.NODE}}` | the last accepted answer of agent node NODE, as JSON |
  | `{{output.NODE.FIELD}}` | one field of that answer |
  | `{{artifact.NODE.FILE}}` | the text of FILE in `HETMAN_RUN_DIR`, if script or gate NODE listed it as an artifact |
  | `{{answers.NODE}}` | what a person last answered at gate or human node NODE: questions, answers, comments, chosen outcome |
  | `{{answers.NODE.ID}}` | the answer to question ID there; `{{answers.NODE.ID.comment}}` its comment |
  | `{{language}}` | the English name of the language for the person (`English` when none is set); `{{language.native}}` its own name, `{{language.code}}` its code |
  | `{{work}}` | the absolute path of the campaign's work folder (`HETMAN_RUN_DIR`), for telling an agent where to write a file for later steps |
  | `{{rework}}` | empty on the first visit; when the flow came back to this node, which step sent it back (looking through script nodes that list no artifacts), with that step's outcome and answer, failed checks, or what a person answered there |

  An input field that does not exist stops the campaign in the error state. A node
  that has not run yet, a missing answer field or an artifact that was not listed
  gives empty text. Put `{{rework}}` in the prompt of every agent node that a
  transition can send the flow back to. `{{` followed by anything that is not a name
  and `}}` is left as text.
- **Run input.** `hetman run -input FILE` gives the campaign a JSON object that
  templates read through `{{input...}}` and every command reads from
  `HETMAN_INPUT_FILE`. It is stored with the campaign, so a resumed campaign sees the
  same input.
- **Describing the run input.** When a flow needs input, describe it in the top-level
  `input` section, so that nobody has to write the JSON by hand: `hetman run -ask`,
  `hetman task new -kind KIND` and the start form of `hetman serve` ask for it field by
  field, and `hetman run -input`, `hetman task add` and the page refuse an input that
  does not fit, naming the field. Without the section nothing is checked.

  ```yaml
  input:
    project: {kind: text, default: shop, ask: false}   # fixed: never asked, always given
    tasks:
      kind: objects                  # a list of objects, each with these fields
      label: {en: Tasks, es: Tareas} # one text, or one per language code
      required: true
      fields:
        id: {kind: text, label: Task id, hint: such as SHOP-12, required: true}
        needs: {kind: list, label: Tasks that must be done first}
        size: {kind: choice, options: [small, large], default: small}
        notes: {kind: longtext}
  ```

  Kinds: `text` (one line), `longtext`, `number`, `yesno`, `choice` (with `options`),
  `list` (a list of texts) and `objects` (with `fields`; a list of objects inside a list
  of objects is not allowed). Per field: `label` and `hint` (a text, or a text per
  language; the person's language is shown, else English, else the first written),
  `required`, `default` and `ask: false` (needs a default). Field names use letters,
  digits, `_` and `-`, no dots. An absent field gets its default. Declare the section
  with the `input-*` operations below rather than by hand.
- **Language for the person.** `language: CODE` at the top level makes what the flow
  addresses to a person (questions, summaries, plans, other documents a person reads)
  come in that language, while code, comments, identifiers, commit messages, file
  names and everything written into the repository stay English. Hetman adds that
  instruction to every agent prompt itself; do not write it into templates. The code
  is one of `ar`, `en`, `es`, `hi`, `uk` and `zh`: the five most spoken languages and
  Ukrainian. Without the key nothing is added. A person can override it for their own
  runs with the environment variable `HETMAN_LANGUAGE`; a campaign keeps the language
  it started with.
- **Roles: several clients for one step.** Instead of `executor`, `model` and
  `effort`, an agent node may name `role: NAME`, a pool declared under the top-level
  `roles`. Naming both is an error. A role must say `select` (no default) and list
  at least one client; each client has its own `executor` (declared under
  `executors`), `model` and `effort`, because a model name means nothing to another
  client. `select` is one of:
  - `fixed`: always the first client that can run;
  - `round_robin`: the clients take turns, per role across the campaign and its
    child campaigns; the turn is kept in the chronicle, so it survives a restart;
  - `quota`: the client that has used the smallest share of its allowance runs. Each
    client's larger share of its five-hour and weekly windows is compared, the
    smaller one breaks a tie, then the order of the list; a client whose share is
    not known at all comes last, and one that has used up a window is skipped until
    the window resets. Shares come from the adapter when it declares the `usage`
    feature (measured), else from Hetman's own count of the attempts it saw against
    `allowance: {five_hour: N, weekly: N}` on the client (estimated; N is a number of
    attempts or a duration of work such as `20h`; `monthly:` instead of `weekly:` is
    converted to a weekly share). Ask the person for an allowance only if they know
    it; never invent one.

  When a client reports a subscription limit, the step moves to the next client that
  can run at once, without using an attempt; a client whose limit resets at a known
  time is skipped by every role until then. Only when no client can run does the
  campaign wait, until the earliest reset, else for a person. A lost connection does
  not change client. A step continued on another client starts a fresh conversation
  with the full prompt and every question a person answered so far.
- **Denied tool calls.** The adapter reports the tool calls its client denied; they
  show in `hetman status`. For a step that must run tests or other commands, set
  `fail_on_denied: [Bash]` on the node (`hetman scribe set NODE fail_on_denied Bash`;
  the client's shell tool, by the name its adapter uses), so that an answer after a denied
  command fails the attempt instead of being accepted, while a harmless denied read
  does not; recommend it to the person for such steps. `true` counts every tool.
- **Thinking takes time.** A node with an `effort` needs an `idle_timeout` of 15
  minutes or more unless its adapter sends progress messages; `hetman check` warns. Give the commands as narrow patterns
  the client can match whole: `Bash(make -C work *)` rather than a `cd work && make`
  the agent might try.
- **Gates tell why they blocked.** The files the checks of a gate list in their result
  files are the gate's artifacts: `{{artifact.GATE.FILE}}`, and `{{rework}}` after a
  blocked gate names the failed checks with their logs and artifacts. Let checks
  write their output to a file in `HETMAN_RUN_DIR` and list it.
- **Attempts.** An answer that does not match the schema, a transient error, a crash,
  a timeout or an idle timeout uses one attempt and the step runs again; after a
  rejected answer the next prompt says what was wrong. When `max_attempts` is used up
  the outcome is `fail`. A permanent error gives `fail` at once. A subscription limit
  or lost connectivity uses no attempt: with a reset time Hetman waits and goes on by
  itself, otherwise the campaign waits for a person and `hetman resume` runs the step
  again. Every visit of a node gets the full number of attempts.

### People in the flow

A flow can stop and ask a person. While it waits, nothing has to keep running; the
question is kept in the chronicle.

```yaml
nodes:
  plan_approved:
    type: gate
    checks: [format]          # optional; run first, the person is asked only if they pass
    ask:
      - id: scope
        question: Does the plan cover every requirement?
      - id: risk
        question: Can the change be undone?
    show: [plan.md, "{{work}}/draft.md"]   # files to look at: relative to orders.yaml, or in the campaign's work folder
    on: {pass: implement, blocked: plan}   # any "no" blocks
  release:
    type: human
    ask:                      # optional; answers are input for later steps
      - {id: channel, question: "Which channel?", kind: choice, options: [stable, beta]}
      - {id: notes, question: "Anything to mention?", kind: text}
    on: {ship: publish, rework: implement, stop: failed}   # the person chooses one
  interview:
    type: agent
    dialogue: 2               # may ask the person questions, up to 2 rounds
    ...
```

- `show` entries are relative to the orders file; one that starts with `{{work}}/`
  is a file in the campaign's own work folder, such as a plan an agent wrote there.
  Never point `show` at a fixed campaign folder or a link to the latest campaign.
- A question has an `id`, the `question` text and a `kind`: `yesno` (default),
  `choice` (needs `options`, at least two) or `text`. A gate asks `yesno` only. Quote
  a question that contains `?`, `:` or `#` inside `{...}`.
- A gate with `ask` passes only when every answer is yes; any no blocks it, and the
  step the flow goes back to sees the answers and comments through `{{rework}}`. Put a
  gate with questions where a person must approve something, after the checks a
  program can do.
- A `human` node's outcome is the one the person chooses from its `on`; its questions
  never decide it. Use it where a person decides where the flow goes.
- `dialogue: N` on an agent node lets the step ask the person questions in the middle
  of its work (a requirements interview, say), at most N rounds; each round continues
  the same conversation with the answers. It needs an adapter that declares the
  `resume` feature; ask the person whether theirs does. Its schema must not list the
  outcome `questions` or have a property `questions`; Hetman adds them itself.
- People answer with `hetman answer` (in the terminal, or with flags such as
  `-a scope=yes -a risk=no -comment risk="..."` and `-outcome ship` for scripts) or on
  the page of `hetman serve`. `hetman status` shows what is asked.
- `notify: {run: [...], timeout: 30s}` names a command Hetman runs whenever a campaign
  starts waiting for a person (a question, lost connectivity, a subscription limit with
  no reset time, the error state). It gets one line of JSON on standard input
  (campaign, node, reason, message, questions, files to show, the command that answers
  or resumes). Offer one only if the person wants to be told in a messenger; it is a
  script they supply or ask you to write, which never blocks the campaign.

### Segments, for-each and locks

A part of a flow used in several places, or once per work item, goes into its own
orders file (a segment), written exactly like `orders.yaml`, and is run as one step:

```yaml
nodes:
  review:
    type: segment
    segment: segments/review.yaml   # relative to this file
    input: output.plan.task         # optional: what the segment gets as its run input
    on: {done: publish, failed: failed}
  build-all:
    type: foreach
    segment: segments/item.yaml
    items: output.plan.items        # a list of objects: {"id": "api", "needs": ["db"], ...}
    parallel: 2                     # at most 2 items at the same time (default 1)
    locks: [release-train]          # optional, as on any node
    on: {done: release, failed: plan}
  test:
    type: script
    run: [./steps/test.sh]
    locks: [test-stand]             # no two steps holding test-stand ever run at once
    on: {ok: done, fail: failed}
```

- A segment runs as a child campaign with its own steps, attempts and folder; its
  end, `done` or `failed`, is the node's outcome. Without `input` it gets the run
  input of the campaign; `input` may name `input.FIELD`, `output.NODE[.FIELD]` of an
  agent node, or `artifact.NODE.FILE` (a JSON file an earlier script listed). It must
  be a JSON object. The steps of a segment run in the folder of the segment's file.
- A `foreach` list comes from an earlier agent output (`output.NODE.FIELD`), a JSON
  file an earlier script listed as an artifact (`artifact.NODE.FILE`), or the run input
  (`input.FIELD`). Every item is an object with a unique `id` and optionally `needs`,
  the ids it must wait for; the whole item is the run input of its child. The node
  ends `done` only when every item did; an item whose needs failed is not run. Tell
  the agent that writes the list about `id` and `needs` in its prompt and schema.
- A segment must not run itself, directly or through other segments.
- `locks` names resources such as a test stand or the one writer of a working copy.
  A step waits until no other step holds any of its locks, in this campaign, other
  campaigns or other hetman processes using the same folder. Locks used inside a
  segment must sort after the locks of the node that runs that segment (rename them,
  for example with a prefix, so they do); `hetman check` says so when they do not.
- People answer, halt, resume, retry and rewind a segment through the campaign that runs it. A retried for-each runs only the items that did not reach done.

### Tasks and the dispatcher

Small internal work with no ticket anywhere (a linter finding on a commit, a remark a
reviewer left) can be queued as tasks; the dispatcher runs each one as an ordinary
campaign of the flow its kind is routed to. Suggest it only when the person has such
work arriving from a script or a scheduler. A routing file, `routes.yaml` next to the
orders files, maps each kind to a flow:

```yaml
version: 0
limit: 2                       # campaigns running at once; 1 when left out
routes:
  machine-finding:
    orders: finding.yaml       # must be in the same folder as routes.yaml
    priority: 10               # higher is taken first; 0 when left out
    limit: 1                   # campaigns of this route at once; no own limit when left out
  reviewer-remark:
    orders: remark.yaml
```

- Every routed orders file is an ordinary flow; write it as any other. Its campaigns
  get the task's input plus `hetman_task` (`id`, `kind`, `key`), so a prompt can say
  `{{input.hetman_task.kind}}` and a script reads it from `HETMAN_INPUT_FILE`. A task
  input must not have that field itself.
- `hetman task check` checks the routing file and every flow it routes to. The routing
  file has no scribe operations; edit it by hand and check it.
- Tasks are added with `hetman task add -kind KIND [-key KEY] -input FILE` (`-input -`
  reads standard input), or with `hetman task new -kind KIND`, which asks for the input
  through the form made from the routed flow's `input` section. A key makes a repeat a no-op while a task with that key exists
  that was not cancelled; `-force` adds it anyway.
- `hetman dispatch` runs the queue (`-once` stops when nothing is left to start). A
  campaign that waits for a person takes no place in the limits; it is answered as
  usual and the dispatcher continues it.

## Changing the file: use the operations

Hetman has a fixed set of operations on `orders.yaml`, run as commands. Use them
instead of editing the YAML yourself wherever one exists. Each one edits the file in
place and keeps everything it does not change (comments, blank lines, order,
alignment), wires the transitions so that the flow stays connected, prints the change
as a diff, and runs the orders check on the result: when the result would not pass,
it writes nothing and prints the problems (exit code 2). Add `-dry-run` to see a
change without writing it, and `-f FILE` when the file is not `orders.yaml` in the
current folder.

```
hetman scribe new TYPE NODE [-flow NAME] [node flags]       start a new file with its first node
hetman scribe add TYPE NODE -after NODE [-via OUTCOME] [node flags]
hetman scribe add TYPE NODE -before NODE [node flags]
hetman scribe remove NODE                    what led to it then leads where its main outcome led
hetman scribe rename NODE NEW                also renames it in the start, transitions and prompt templates
hetman scribe link NODE OUTCOME TARGET       set or change a transition (TARGET: a node, done or failed);
                                             -max N -then OTHER limits it to N times, then OTHER
hetman scribe unlink NODE OUTCOME            remove a transition
hetman scribe set NODE FIELD VALUE           run, timeout, idle_timeout, executor, role, model, effort,
                                             tools, attempts, dialogue, prompt, schema, workdir,
                                             fail_on_denied, locks, segment, input, items, parallel
hetman scribe unset NODE FIELD               timeout, idle_timeout, model, effort, tools, attempts, dialogue,
                                             workdir, fail_on_denied, locks, input, parallel
hetman scribe check NAME -run CMD [-timeout D] [-soft | -required] [-locks A,B]   declare a check or change one
hetman scribe executor NAME -run CMD         declare an executor or change its command
hetman scribe gate-add GATE CHECK [-at N]    put a declared check into a gate, at position N from 1
hetman scribe gate-remove GATE CHECK
hetman scribe ask NODE ID QUESTION [-kind yesno|choice|text] [-options A,B]
hetman scribe role NAME -select MODE -executor X [-model M] [-effort E]   declare a role with its first client
hetman scribe role NAME -select MODE         change the mode of a declared role (fixed, round_robin, quota)
hetman scribe role-add ROLE EXECUTOR [-model M] [-effort E] [-at N]   add a client to a role
hetman scribe role-remove ROLE N             remove the client at position N from 1 (a role keeps one)
hetman scribe assistant EXECUTOR MODEL [-effort E]   the model hetman scribe may ask
hetman scribe language CODE                  the language for the person, such as es
hetman scribe unlanguage                     remove it: English, nothing added to prompts
hetman scribe workdir FOLDER                 the folder agent steps work in, such as ..
hetman scribe show-add NODE FILE             show a file at a gate or human node; {{work}}/FILE for the work folder
hetman scribe show-remove NODE FILE          stop showing it
hetman scribe input-add NAME KIND [-of LIST] [-label TEXT] [-hint TEXT] [-language CODE]
                       [-required] [-default VALUE [-ask=false]] [-options A,B]
                                             add a field to the run input; -of puts it in a list of
                                             objects, which its first field creates
hetman scribe input-remove NAME [-of LIST]   remove a field; the section goes with its last field
hetman scribe input-set NAME FIELD VALUE [-of LIST] [-language CODE]
                                             FIELD: label, hint, required, default or ask
hetman scribe input-unset NAME FIELD [-of LIST] [-language CODE]
                                             FIELD: label, hint or default
```

Node flags: `-run 'CMD ARGS'` (one string, split at spaces; quote an argument that has
spaces), `-timeout 5m`, `-checks a,b` (declared checks, in order), `-executor NAME`,
`-role NAME` (instead of `-executor`, `-model` and `-effort`), `-model M`, `-effort E`, `-tools a,b`, `-attempts N`, `-prompt FILE`, `-schema FILE`,
`-outcomes a,b` (of an agent or human node), `-on OUTCOME=TARGET` (repeat it; every
outcome other than the main one leads to `failed` unless given), `-starters`
(create a minimal prompt template and schema for an agent node where those files do
not exist yet; it never overwrites a file), `-locks a,b` (any node), `-segment FILE`
(segment and foreach; the file must exist), `-input REF` (segment), `-items REF` and
`-parallel N` (foreach), `-workdir FOLDER` (an agent node's own folder; for `new`, the
default written at the top, `.` when not given). A segment or foreach node's main
outcome is `done`. A list flag ends an entry only at a comma or space outside
parentheses and quotes: `-tools 'Read,Bash(make -C *),Bash(go test *)'` gives three
entries.

How they wire the flow:

- The main outcome of a node is `ok`, else `pass`, else its first outcome that is not
  `fail`, `timeout` or `blocked` (the first in an agent's schema, the first choice of a
  human node). `add -after X` makes X's main outcome (or `-via OUTCOME`) lead to the
  new node, and the new node's main outcome lead where X's did. `add -before Y` makes
  the start, if it was Y, and every main outcome that led to Y lead to the new node,
  whose main outcome leads to Y; transitions back to Y (rework loops) stay as they are.
- `set NODE timeout` on a script adds `timeout: failed` when the node has no
  transition for `timeout`; `unset NODE timeout` removes it.
- Declare a check before a gate uses it, an executor before an agent node, a role
  or `assistant` names it, and a role before a node names it.
- `set NODE role R` removes the node's `executor`, `model` and `effort`; `set NODE
  executor X` on a node run through a role removes its `role`. The model and effort
  of a node run through a role belong to the role's clients: change them with
  `role-remove` and `role-add`. An allowance is written by hand.

Examples:

```sh
hetman scribe new script build -flow blog -run 'make build' -timeout 5m
hetman scribe check spelling -run './checks/spell.sh' -soft
hetman scribe check links -run './checks/links.sh' -timeout 15m
hetman scribe add gate checked -after build -checks spelling,links
hetman scribe add script upload -after checked -run './upload.sh' -timeout 10m
hetman scribe executor coder -run './adapters/my-adapter'
hetman scribe add agent review -after build -executor coder -outcomes approved,needs_rework -on needs_rework=build -starters
```

Some changes need two operations and a state in between that does not pass the
check, such as a gate that only asks a person (add it with `-force`, then `ask`).
`-force` writes such a state; finish it with the next operation and make sure
`hetman check` passes in the end. Do not use `-force` to get past a problem you do not
understand.

There is no operation for the content of prompt templates and schemas, for a client's
`allowance`, for `show`,
`notify`, comments, or moving a node to another place in the file. Edit those by hand,
keep the rest of the file as it is, and run `hetman check` afterwards. Never edit by
hand what an operation can do.

`hetman scribe explain [-c ID] [-kind where|history|rework|failures|warnings]` tells
in plain sentences, from the chronicle, where a campaign is, what ran, why it went back
and what failed; use it to answer questions about a campaign.

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
   gate placed where the verification has to happen; steps a coding agent does
   (write code, review, summarise) become `agent` nodes; points where a person
   approves become a gate with `ask`; points where a person decides where the flow
   goes become a `human` node. For an agent step ask what
   the agent should be told, what it must answer (this becomes the schema), and
   which answers lead where (these become the outcomes). Checks in a gate get the
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
7. **Language.** If the flow asks a person questions or writes documents for one:
   "In which language should the person read them?" Set it with
   `hetman scribe language CODE`; leave it out for English.
8. **Input.** If a template or script reads `{{input...}}` or `HETMAN_INPUT_FILE`:
   "What does a person give when starting a campaign?" Declare each field with
   `hetman scribe input-add`, so the person can start work through a form.

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
- **An agent never checks itself.** Tests, linters and anything with side effects
  (pushing, publishing, opening tickets) are script nodes or gate checks run by
  Hetman, never a claim in an agent's answer. Put a gate after an agent step whose
  work must be verified.
- **Give agent steps limits.** Set `timeout` and `idle_timeout` on agent nodes so that
  a stuck client is noticed, and keep `max_attempts` small.
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
gate holds them. You build the file with the operations:

```sh
hetman scribe new script build -flow blog -run 'make build' -timeout 5m
hetman scribe check spelling -run ./checks/spell.sh -soft
hetman scribe check links -run ./checks/links.sh -timeout 15m
hetman scribe add gate checked -after build -checks spelling,links
hetman scribe add script upload -after checked -run ./upload.sh -timeout 10m
```

and add the three comments by hand. The result:

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

`hetman check` also looks for the program of every `run` list. A program written
with a path must exist and be executable, or the file is rejected. A bare program
name (`make`, `python3`) that is not on this computer's path is reported as
`FILE:LINE:COLUMN: warning: ...` and does not fail the check, because the flow may
run on another computer. When you see such a warning, ask the person whether the
program is installed where the flow will run.

`hetman check` also reads every prompt template and schema: an unknown placeholder or a
schema outside the supported subset is reported at its line in that file.

What `hetman check` cannot see, confirm yourself before you finish: that a script
passed as an argument to an interpreter (`steps/build.sh` in `[sh, steps/build.sh]`)
exists, that every outcome a script writes to the result file has a transition, and
that the fields a template reads from the run input exist in the input file.

Then show the person the flow in plain words, one line per step, for example:

> First the site is built (at most 5 minutes). Then the spelling is checked, which
> only warns, and the links are checked, which must pass. If they pass, the site is
> uploaded and the run is done. Any failure ends the run.

Mention the choices you made on their behalf, and tell them how to use it:

- `hetman run -f path/to/orders.yaml` starts a new campaign and runs it to the end;
  add `-input FILE` when templates read the run input.
- `hetman status -f path/to/orders.yaml` shows where the latest campaign is, and what
  it asks a person, if anything; `hetman answer -f path/to/orders.yaml` answers it.
- `hetman halt -f path/to/orders.yaml` stops the latest campaign after its current
  step; `hetman resume -f path/to/orders.yaml` continues a halted, stopped or
  interrupted one. Both take `-c ID` for an older campaign.
- When run from the folder that holds `orders.yaml`, every command can leave out `-f`.
- Hetman keeps its journal and each campaign's files in a `.hetman/` folder next to
  `orders.yaml`; suggest adding it to the version control ignore list.

Do not start a campaign yourself unless the person asks; steps with side effects
would really run.
