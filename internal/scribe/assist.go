package scribe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"hetman/internal/edit"
	"hetman/internal/engine"
	"hetman/internal/explain"
	"hetman/internal/i18n"
	"hetman/internal/language"
	"hetman/internal/orders"
	"hetman/internal/schema"
)

// Model answers one request with JSON that should match the schema it is
// given. The attempt is 1, or 2 for the one retry after an invalid answer.
type Model interface {
	Ask(ctx context.Context, prompt string, schema []byte, attempt int) (json.RawMessage, error)
}

// DefaultTimeout limits a request when the scribe setting gives no timeout.
const DefaultTimeout = 5 * time.Minute

// AdapterModel asks the executor and model named by the scribe setting of
// the orders, through the adapter protocol.
type AdapterModel struct{ Orders *orders.Orders }

// Ask sends one request.
func (m AdapterModel) Ask(ctx context.Context, prompt string, schema []byte, attempt int) (json.RawMessage, error) {
	s := m.Orders.Scribe
	timeout := s.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return engine.Consult(ctx, m.Orders, engine.Request{Executor: s.Executor, Model: s.Model, Effort: s.Effort,
		Prompt: prompt, Schema: schema, Attempt: attempt, Timeout: timeout})
}

// What a model may answer, in the field "outcome".
const (
	AnswerOperation = "operation" // one change to the flow
	AnswerQuery     = "query"     // one question about a campaign
	AnswerUnclear   = "unclear"   // up to three interpretations to choose from
)

// MaxChoices is the most interpretations an unclear answer may offer.
const MaxChoices = 3

// Query is a question about a campaign: one of the explain kinds, the
// campaign (0 for the latest) and optionally a node.
type Query struct {
	Kind     string `json:"kind"`
	Campaign int64  `json:"campaign,omitempty"`
	Node     string `json:"node,omitempty"`
}

// Words describes a query in plain words.
func (q Query) Words() string { return q.WordsIn(nil) }

// WordsIn describes the query in plain words in the language of p.
func (q Query) WordsIn(p *i18n.Printer) string {
	which := p.T("query.latest")
	if q.Campaign > 0 {
		which = p.T("query.campaign", "id", q.Campaign)
	}
	var s string
	switch q.Kind {
	case explain.Summary, explain.Where, explain.History, explain.Rework, explain.Failures, explain.Warnings:
		s = p.T("query."+q.Kind, "which", which)
	default:
		s = p.T("query.other", "kind", q.Kind, "which", which)
	}
	if q.Node != "" {
		s += p.T("query.at_node", "node", q.Node)
	}
	return s
}

// Choice is one interpretation of an unclear sentence.
type Choice struct {
	Question  string   `json:"question"`
	Operation *edit.Op `json:"operation,omitempty"`
	Query     *Query   `json:"query,omitempty"`
}

// Answer is what the model returns for a sentence.
type Answer struct {
	Outcome   string   `json:"outcome"`
	Operation *edit.Op `json:"operation,omitempty"`
	Query     *Query   `json:"query,omitempty"`
	Choices   []Choice `json:"choices,omitempty"`
}

func opSchema() map[string]any {
	str := map[string]any{"type": "string"}
	list := map[string]any{"type": "array", "items": str}
	var ops []string
	for _, s := range edit.Specs {
		if s.Op != edit.OpNew { // a sentence changes an existing flow
			ops = append(ops, s.Op)
		}
	}
	props := map[string]any{"op": map[string]any{"type": "string", "enum": ops}}
	for _, f := range []string{"node", "type", "after", "via", "before", "name", "outcome", "target", "field", "value",
		"check", "id", "question", "kind", "flow", "timeout", "executor", "role", "model", "effort", "prompt", "schema", "segment", "input", "items", "workdir", "then"} {
		props[f] = str
	}
	for _, f := range []string{"options", "run", "checks", "tools", "outcomes", "locks"} {
		props[f] = list
	}
	props["at"] = map[string]any{"type": "integer"}
	props["attempts"] = map[string]any{"type": "integer"}
	props["parallel"] = map[string]any{"type": "integer"}
	props["max"] = map[string]any{"type": "integer"}
	props["soft"] = map[string]any{"type": "boolean"}
	props["starters"] = map[string]any{"type": "boolean"}
	props["on"] = map[string]any{"type": "object", "additionalProperties": str}
	props["language"] = map[string]any{"type": "string", "enum": language.Codes()}
	props["select"] = map[string]any{"type": "string", "enum": orders.SelectModes}
	return map[string]any{"type": "object", "properties": props, "required": []string{"op"}, "additionalProperties": false}
}

func querySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"kind":     map[string]any{"type": "string", "enum": explain.Kinds},
		"campaign": map[string]any{"type": "integer"},
		"node":     map[string]any{"type": "string"},
	}, "required": []string{"kind"}, "additionalProperties": false}
}

// Schema is the JSON schema every answer must match.
func Schema() []byte {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome":   map[string]any{"type": "string", "enum": []string{AnswerOperation, AnswerQuery, AnswerUnclear}},
			"operation": opSchema(),
			"query":     querySchema(),
			"choices": map[string]any{"type": "array", "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question":  map[string]any{"type": "string"},
					"operation": opSchema(),
					"query":     querySchema(),
				},
				"required":             []string{"question"},
				"additionalProperties": false,
			}},
		},
		"required":             []string{"outcome"},
		"additionalProperties": false,
	}
	data, _ := json.Marshal(s)
	return data
}

// Describe is the compact description of a flow a model is given: node
// names, types, transitions, checks and executors. It never holds the text
// of the file.
func Describe(o *orders.Orders, campaigns []int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "flow %s, starts at %s\n", o.Flow, o.Start)
	if l, ok := language.Lookup(o.Language); ok {
		fmt.Fprintf(&b, "language for the person: %s (%s)\n", l.Code, l.English)
	}
	b.WriteString("nodes, in file order:\n")
	for _, id := range o.Order {
		n := o.Nodes[id]
		var facts []string
		switch n.Type {
		case orders.Gate:
			if len(n.Checks) > 0 {
				facts = append(facts, "checks "+strings.Join(n.Checks, ", "))
			}
			if len(n.Run) > 0 {
				facts = append(facts, "runs a command")
			}
		case orders.Agent:
			if n.Role != "" {
				facts = append(facts, "role "+n.Role)
				break
			}
			s := "executor " + n.Executor
			if n.Model != "" {
				s += ", model " + n.Model
			}
			if n.Effort != "" {
				s += ", effort " + n.Effort
			}
			facts = append(facts, s)
		}
		if len(n.Ask) > 0 {
			var ids []string
			for _, q := range n.Ask {
				ids = append(ids, q.ID)
			}
			facts = append(facts, "asks "+strings.Join(ids, ", "))
		}
		if n.Timeout > 0 {
			facts = append(facts, "timeout "+short(n.Timeout))
		}
		var on []string
		for _, out := range outcomes(n) {
			on = append(on, out+" -> "+n.On[out])
		}
		facts = append(facts, strings.Join(on, ", "))
		fmt.Fprintf(&b, "  %s: %s; %s\n", id, n.Type, strings.Join(facts, "; "))
	}
	var checks []string
	for _, name := range sortedKeys(o.Checks) {
		s := name
		if o.Checks[name].Soft {
			s += " (soft)"
		}
		checks = append(checks, s)
	}
	fmt.Fprintf(&b, "declared checks: %s\n", orNone(checks))
	fmt.Fprintf(&b, "declared executors: %s\n", orNone(sortedKeys(o.Executors)))
	var roles []string
	for _, name := range sortedKeys(o.Roles) {
		r := o.Roles[name]
		var cs []string
		for i, c := range r.Clients {
			cs = append(cs, c.Label(i))
		}
		roles = append(roles, fmt.Sprintf("%s (%s: %s)", name, r.Select, strings.Join(cs, ", ")))
	}
	fmt.Fprintf(&b, "declared roles: %s\n", orNone(roles))
	if n := len(campaigns); n > 0 {
		var ids []string
		for _, id := range campaigns {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		fmt.Fprintf(&b, "campaigns: %s (the latest is %d)\n", strings.Join(ids, ", "), campaigns[n-1])
	} else {
		b.WriteString("campaigns: none yet\n")
	}
	return b.String()
}

// short writes a duration without zero units: 5m, not 5m0s.
func short(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// outcomes lists a node's transitions: those the engine knows first, then
// the rest by name.
func outcomes(n *orders.Node) []string {
	var out []string
	for _, o := range n.Outcomes() {
		if _, ok := n.On[o]; ok {
			out = append(out, o)
		}
	}
	var rest []string
	for o := range n.On {
		if !containsStr(out, o) {
			rest = append(rest, o)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func sortedKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// fieldHelp says what each field of an operation holds.
const fieldHelp = `  node: a node id (for new and add: the new node's id); type: script, gate, agent or human;
  after, before: an existing node; via: the outcome of "after" that leads to the new node (default its main outcome);
  name: the new name (rename), or the check or executor declared; outcome: an outcome name; target: a node, done or failed;
  field and value: the field set or removed and its value as text; check: a declared check; at: position in the gate from 1;
  id, question, kind (yesno, choice, text), options: a question; flow: the flow name;
  run: the command as a list, program first; timeout: a duration such as 30s, 5m, 1h;
  checks: declared checks in order; executor, model, effort, tools, attempts, prompt, schema: agent settings;
  role: a declared role whose clients run an agent node, instead of executor, model and effort;
  name and select for role, role-add and role-remove: the role and its mode (fixed, round_robin or quota); at: a client's position from 1;
  outcomes: the outcomes of an agent or human node; on: transitions other than the defaults, as {"outcome": "target"};
  soft: true for a check that only warns, false for a required one; starters: true to create missing prompt and schema files;
  max and then (link): the transition may be taken max times per campaign, then the outcome then is taken instead;
  workdir: the folder agent steps work in, relative to the orders folder (workdir; add: one agent node's own);
  language: the code of the language for the person, one of ` + "%s" + `.
`

// Prompt is the request for one sentence.
func Prompt(sentence string, o *orders.Orders, campaigns []int64) string {
	var b strings.Builder
	b.WriteString(`You map one sentence about a Hetman flow to exactly one item from fixed lists. You do not see or write any file and you have no tools. Reply with a single JSON object that matches the given schema, and nothing else.

The sentence:
  ` + strings.ReplaceAll(strings.TrimSpace(sentence), "\n", " ") + `

The flow:
`)
	for _, line := range strings.Split(strings.TrimSuffix(Describe(o, campaigns), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString(`
Answer with "outcome" and one of these:

1. "outcome": "operation", with "operation": one change to the flow. "op" is one of:
`)
	for _, s := range edit.Specs {
		if s.Op == edit.OpNew {
			continue
		}
		parts := []string{}
		if len(s.Required) > 0 {
			parts = append(parts, "needs "+strings.Join(s.Required, ", "))
		}
		if len(s.Optional) > 0 {
			parts = append(parts, "may have "+strings.Join(s.Optional, ", "))
		}
		fmt.Fprintf(&b, "  %s: %s (%s)\n", s.Op, s.Summary, strings.Join(parts, "; "))
	}
	b.WriteString(`  add needs exactly one of after and before. Fields:
` + fmt.Sprintf(fieldHelp, strings.Join(language.Codes(), ", ")) + `
2. "outcome": "query", with "query": a question about a campaign. "kind" is one of:
  where (where it is, what it waits for, since when), history (what ran with what outcome),
  rework (why the flow went back and how many times), failures (what failed and why),
  warnings (which soft checks warned), summary (all of these).
  "campaign" is its number (leave it out for the latest); "node" limits the answer to one node.

3. "outcome": "unclear", with "choices": when the sentence can mean different things or lacks something
  an operation needs, up to three interpretations. Each has "question", a short question to the person
  ("Do you mean ...?"), and the "operation" or the "query" it stands for.

Use only the node, check and executor names listed above, except a name the sentence asks to create.
Never guess: if a name or a value the operation needs is missing, answer "unclear".
`)
	return b.String()
}

var answerSchema = func() *schema.Schema {
	s, err := schema.Parse(Schema())
	if err != nil {
		panic(err)
	}
	return s
}()

// parse checks an answer against the schema and the rules of each outcome.
func parse(raw json.RawMessage) (Answer, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return Answer{}, fmt.Errorf("not JSON: %v", err)
	}
	if problems := answerSchema.Validate(v); len(problems) > 0 {
		return Answer{}, errors.New(strings.Join(problems, "; "))
	}
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return Answer{}, err
	}
	switch a.Outcome {
	case AnswerOperation:
		if a.Operation == nil || a.Query != nil || len(a.Choices) > 0 {
			return a, errors.New(`"operation" answers need exactly the field operation`)
		}
		return a, a.Operation.Validate()
	case AnswerQuery:
		if a.Query == nil || a.Operation != nil || len(a.Choices) > 0 {
			return a, errors.New(`"query" answers need exactly the field query`)
		}
		return a, a.Query.validate()
	default:
		if len(a.Choices) == 0 || len(a.Choices) > MaxChoices || a.Operation != nil || a.Query != nil {
			return a, fmt.Errorf(`"unclear" answers need choices, from 1 to %d, and nothing else`, MaxChoices)
		}
		for i, c := range a.Choices {
			if (c.Operation == nil) == (c.Query == nil) || strings.TrimSpace(c.Question) == "" {
				return a, fmt.Errorf("choice %d needs a question and exactly one of operation and query", i+1)
			}
			if c.Operation != nil {
				if err := c.Operation.Validate(); err != nil {
					return a, fmt.Errorf("choice %d: %v", i+1, err)
				}
			} else if err := c.Query.validate(); err != nil {
				return a, fmt.Errorf("choice %d: %v", i+1, err)
			}
		}
		return a, nil
	}
}

func (q Query) validate() error {
	if !containsStr(explain.Kinds, q.Kind) {
		return fmt.Errorf("query kind %q is not one of %s", q.Kind, strings.Join(explain.Kinds, ", "))
	}
	if q.Campaign < 0 {
		return errors.New("campaign numbers start at 1")
	}
	return nil
}

// ErrInvalid is returned when the model gave no usable answer, even after
// its one retry.
var ErrInvalid = errors.New("the model gave no usable answer")

// Interpret asks the model once for a sentence, and once more if that
// answer is not valid.
func Interpret(ctx context.Context, m Model, sentence string, o *orders.Orders, campaigns []int64) (Answer, error) {
	prompt := Prompt(sentence, o, campaigns)
	var last error
	for attempt := 1; attempt <= 2; attempt++ {
		p := prompt
		if attempt > 1 {
			p += fmt.Sprintf("\nYour previous answer was rejected: %v. Reply again with a single JSON object that matches the schema.\n", last)
		}
		raw, err := m.Ask(ctx, p, Schema(), attempt)
		if err != nil {
			return Answer{}, fmt.Errorf("asking the model: %w", err)
		}
		a, err := parse(raw)
		if err == nil {
			return a, nil
		}
		last = err
	}
	return Answer{}, fmt.Errorf("%w: %v", ErrInvalid, last)
}

// Errors of a sentence that changed nothing.
var (
	ErrDeclined = errors.New("not confirmed; nothing was changed")
	ErrUnclear  = errors.New("the sentence is unclear; nothing was changed")
	ErrRejected = errors.New("the answer cannot be used; nothing was changed")
)

// Assistant handles one sentence: it asks the model, shows what the answer
// means and applies an operation only after the person confirms it.
type Assistant struct {
	Orders    *orders.Orders
	Model     Model
	In        *bufio.Reader
	Out       io.Writer
	Yes       bool    // confirm a clear operation without asking
	Echo      bool    // write each answer read, for input that is not typed
	Campaigns []int64 // the campaigns in the chronicle, oldest first
	// Explain answers a query, or returns an error for a campaign or node
	// that does not exist.
	Explain func(Query) (string, error)
	// Text is the language of what the person reads; nil for English.
	Text *i18n.Printer
}

// Handle maps a sentence to an operation or a query and carries it out.
func (a *Assistant) Handle(ctx context.Context, sentence string) error {
	ans, err := Interpret(ctx, a.Model, sentence, a.Orders, a.Campaigns)
	if err != nil {
		fmt.Fprintln(a.Out, a.Text.T("assist.unusable", "error", err))
		return err
	}
	switch ans.Outcome {
	case AnswerOperation:
		return a.operation(*ans.Operation)
	case AnswerQuery:
		return a.query(*ans.Query)
	}
	p := a.Text
	fmt.Fprintln(a.Out, p.T("assist.unclear"))
	for i, c := range ans.Choices {
		what := ""
		if c.Operation != nil {
			what = c.Operation.WordsIn(p)
		} else {
			what = c.Query.WordsIn(p)
		}
		fmt.Fprintf(a.Out, "  %d) %s\n     (%s)\n", i+1, strings.TrimSpace(c.Question), what)
	}
	fmt.Fprintln(a.Out, "  0) "+p.T("assist.none"))
	if a.Yes {
		fmt.Fprintln(a.Out, p.T("assist.needs_choice"))
		return ErrUnclear
	}
	fmt.Fprint(a.Out, p.T("assist.choice", "max", len(ans.Choices))+" ")
	line := a.read()
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(ans.Choices) {
		fmt.Fprintln(a.Out, p.T("assist.nothing_changed"))
		return ErrUnclear
	}
	c := ans.Choices[n-1]
	if c.Operation != nil {
		return a.operation(*c.Operation)
	}
	return a.query(*c.Query)
}

func (a *Assistant) operation(op edit.Op) error {
	p := a.Text
	fmt.Fprintln(a.Out, p.T("assist.proposes", "words", op.WordsIn(p)))
	doc, err := edit.Open(a.Orders.Path)
	if err != nil {
		return err
	}
	if err := doc.Apply(op); err != nil {
		fmt.Fprintln(a.Out, p.T("assist.cannot_apply", "error", err))
		return ErrRejected
	}
	if _, err := doc.Check(); err != nil {
		fmt.Fprintln(a.Out, p.T("assist.check_fails", "problems", err))
		return ErrRejected
	}
	fmt.Fprint(a.Out, doc.Diff())
	for _, n := range doc.NotesIn(p) {
		fmt.Fprintln(a.Out, p.T("op.note", "note", n))
	}
	if !a.Yes {
		fmt.Fprint(a.Out, p.T("assist.apply")+" ")
		line := a.read()
		if !i18n.IsYes(p, line) {
			fmt.Fprintln(a.Out, p.T("assist.nothing_changed"))
			return ErrDeclined
		}
	}
	if err := doc.Save(false); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, p.T("op.wrote", "path", doc.Path))
	return nil
}

// read reads one answer of the person.
func (a *Assistant) read() string {
	line, err := a.In.ReadString('\n')
	if a.Echo || err != nil && line == "" {
		fmt.Fprintln(a.Out, strings.TrimRight(line, "\r\n"))
	}
	return line
}

func (a *Assistant) query(q Query) error {
	if a.Explain == nil {
		fmt.Fprintln(a.Out, a.Text.T("assist.no_campaigns"))
		return ErrRejected
	}
	text, err := a.Explain(q)
	if err != nil {
		fmt.Fprintln(a.Out, a.Text.T("assist.understood", "words", q.WordsIn(a.Text), "error", err))
		return ErrRejected
	}
	fmt.Fprint(a.Out, text)
	return nil
}
