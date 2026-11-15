package watchtower

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/edit"
	"hetman/internal/engine"
	"hetman/internal/explain"
	"hetman/internal/i18n"
	"hetman/internal/language"
	"hetman/internal/orders"
	"hetman/internal/scribe"
)

// The routes that start campaigns, send them back and change the flow. All
// of them change state, so each one has the cross-site protection of halt,
// resume and answer.
func (s *Server) controlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/flows", s.handleFlows)
	mux.HandleFunc("GET /api/flows/{file}", s.handleFlow)
	mux.Handle("POST /api/flows/{file}/start", s.control(s.handleStart))
	mux.Handle("POST /api/flows/{file}/preview", s.control(s.handlePreview))
	mux.Handle("POST /api/flows/{file}/apply", s.control(s.handleApply))
	mux.Handle("POST /api/flows/{file}/sentence", s.control(s.handleSentence))
	mux.Handle("POST /api/campaigns/{id}/retry", s.control(s.handleRetry))
	mux.Handle("POST /api/campaigns/{id}/rewind", s.control(s.handleRewind))
}

var (
	errNoFlow    = errors.New("no such orders file in the folder of this server")                           // i18n: keep (API error)
	errBadInput  = errors.New("the run input must be a JSON object")                                        // i18n: keep (API error)
	errBadBody   = errors.New("the request body is not valid")                                              // i18n: keep (API error)
	errStale     = errors.New("the orders file changed since the change was shown; look at it again")       // i18n: keep (API error)
	errNewOnPage = errors.New("the page changes existing orders files; start a new one with hetman scribe") // i18n: keep (API error)
)

// FlowFile is an orders file in the folder of the server.
type FlowFile struct {
	File            string    `json:"file"` // its name in the folder
	Flow            string    `json:"flow,omitempty"`
	Own             bool      `json:"own"`               // the file the server was started with
	Problem         string    `json:"problem,omitempty"` // it does not pass the orders check
	Language        *Language `json:"language,omitempty"`
	LanguageProblem string    `json:"languageProblem,omitempty"`
	Scribe          bool      `json:"scribe"` // it names an executor and model for Scribe

	path string
	o    *orders.Orders
}

// flowFiles lists the orders files of the server's folder: the file it was
// started with, and every other YAML file there that passes the orders
// check. Only these can be started or changed from the page.
func (s *Server) flowFiles() []FlowFile {
	dir := filepath.Dir(s.OrdersPath)
	own := filepath.Base(s.OrdersPath)
	entries, _ := os.ReadDir(dir)
	var out []FlowFile
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !e.Type().IsRegular() || ext != ".yaml" && ext != ".yml" || strings.HasPrefix(name, ".") {
			continue
		}
		f := FlowFile{File: name, Own: name == own, path: filepath.Join(dir, name)}
		o, err := orders.Load(f.path)
		if err != nil {
			if !f.Own {
				continue
			}
			f.Problem = err.Error()
		} else {
			f.o, f.Flow, f.Scribe = o, o.Flow, o.Scribe != nil
			code, err := s.startLanguage(o)
			if err != nil {
				f.LanguageProblem = err.Error()
			} else if code != "" {
				l, _ := language.Lookup(code)
				f.Language = &Language{Code: code, English: l.English, Native: l.Native}
			}
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Own && !out[j].Own })
	return out
}

func (s *Server) flowFile(name string) (FlowFile, error) {
	for _, f := range s.flowFiles() {
		if f.File == name {
			return f, nil
		}
	}
	return FlowFile{}, errNoFlow
}

func (s *Server) startLanguage(o *orders.Orders) (string, error) {
	if s.Language != nil {
		return s.Language(o)
	}
	return o.Language, nil
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.flowFiles())
}

// Flow is the pipeline of an orders file and what its editor offers.
type Flow struct {
	FlowFile
	Columns   []Column            `json:"columns"`
	Nodes     []FlowNode          `json:"nodes"` // in file order
	Checks    []FlowCheck         `json:"checks"`
	Executors []string            `json:"executors"`
	Roles     []FlowRole          `json:"roles"`
	Modes     []string            `json:"modes"`  // the selection modes a role may have
	Fields    map[string][]string `json:"fields"` // the fields set and unset change
	Active    []int64             `json:"active"` // campaigns of this file that have not ended
	Base      string              `json:"base"`   // fingerprint of the file as read
}

// FlowNode is one node as the editor shows it.
type FlowNode struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	On       map[string]string `json:"on"`
	Outcomes []string          `json:"outcomes"` // every outcome it can give, with or without a transition
	Run      []string          `json:"run,omitempty"`
	Checks   []string          `json:"checks,omitempty"`
	Timeout  string            `json:"timeout,omitempty"`
	Executor string            `json:"executor,omitempty"`
	Role     string            `json:"role,omitempty"`
	Model    string            `json:"model,omitempty"`
	Effort   string            `json:"effort,omitempty"`
	Attempts int               `json:"attempts,omitempty"`
	Ask      []string          `json:"ask,omitempty"` // question ids
}

// FlowRole is a declared role.
type FlowRole struct {
	Name    string       `json:"name"`
	Select  string       `json:"select"`
	Clients []FlowClient `json:"clients"`
}

// FlowClient is one client of a role.
type FlowClient struct {
	Executor string `json:"executor"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// FlowCheck is a declared check.
type FlowCheck struct {
	Name string `json:"name"`
	Soft bool   `json:"soft"`
}

func (s *Server) handleFlow(w http.ResponseWriter, r *http.Request) {
	f, err := s.flowFile(r.PathValue("file"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	fl := Flow{FlowFile: f, Fields: map[string][]string{"set": edit.SetFields(), "unset": edit.UnsetFields()},
		Base: fingerprint(f.path), Active: s.active(f.path)}
	if o := f.o; o != nil {
		fl.Columns = build(chronicle.Campaign{Flow: o.Flow, Orders: o.Path}, nil, o, "", func() bool { return false }).Columns
		for _, id := range o.Order {
			n := o.Nodes[id]
			fn := FlowNode{ID: id, Type: n.Type, On: n.On, Run: n.Run, Checks: n.Checks, Executor: n.Executor, Role: n.Role,
				Model: n.Model, Effort: n.Effort, Attempts: n.MaxAttempts}
			if n.Timeout > 0 {
				fn.Timeout = n.Timeout.String()
			}
			for _, q := range n.Ask {
				fn.Ask = append(fn.Ask, q.ID)
			}
			fn.Outcomes = append(fn.Outcomes, n.Outcomes()...)
			for out := range n.On {
				if !contains(fn.Outcomes, out) {
					fn.Outcomes = append(fn.Outcomes, out)
				}
			}
			fl.Nodes = append(fl.Nodes, fn)
		}
		for name, c := range o.Checks {
			fl.Checks = append(fl.Checks, FlowCheck{Name: name, Soft: c.Soft})
		}
		sort.Slice(fl.Checks, func(i, j int) bool { return fl.Checks[i].Name < fl.Checks[j].Name })
		for name := range o.Executors {
			fl.Executors = append(fl.Executors, name)
		}
		sort.Strings(fl.Executors)
		fl.Roles = []FlowRole{}
		for name, r := range o.Roles {
			fr := FlowRole{Name: name, Select: r.Select}
			for _, c := range r.Clients {
				fr.Clients = append(fr.Clients, FlowClient{Executor: c.Executor, Model: c.Model, Effort: c.Effort})
			}
			fl.Roles = append(fl.Roles, fr)
		}
		sort.Slice(fl.Roles, func(i, j int) bool { return fl.Roles[i].Name < fl.Roles[j].Name })
	}
	fl.Modes = orders.SelectModes
	writeJSON(w, http.StatusOK, fl)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// fingerprint identifies the content of a file, so that a change shown on
// the page is written only to the file it was made on.
func fingerprint(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// active lists the campaigns started with an orders file that have not
// ended: they read the file again when they continue.
func (s *Server) active(path string) []int64 {
	list, err := s.Chron.Campaigns(listLimit)
	if err != nil {
		return nil
	}
	out := []int64{}
	for _, k := range list {
		if k.Orders != path {
			continue
		}
		events, err := s.Chron.Events(k.ID)
		if err == nil && chronicle.Replay(events).End == "" {
			out = append(out, k.ID)
		}
	}
	return out
}

// decode reads a JSON request body into v; an empty body leaves v as it is.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %v", errBadBody, err)
	}
	return nil
}

// StartRequest is the body of a start request.
type StartRequest struct {
	Input json.RawMessage `json:"input"` // a JSON object; absent for none
}

// handleStart starts a campaign of an orders file and drives it, after the
// same checks as the run command. Problems of the orders are sent back
// instead of starting.
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	f, err := s.flowFile(r.PathValue("file"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	var req StartRequest
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var input json.RawMessage
	if len(bytes.TrimSpace(req.Input)) > 0 {
		var obj map[string]json.RawMessage
		var compact bytes.Buffer
		if json.Unmarshal(req.Input, &obj) != nil || obj == nil || json.Compact(&compact, req.Input) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errBadInput.Error()})
			return
		}
		input = compact.Bytes()
	}
	o, err := orders.Load(f.path)
	var problems []string
	var ps orders.Problems
	switch {
	case errors.As(err, &ps):
		problems = problemLines(ps)
	case err != nil:
		problems = []string{err.Error()}
	default:
		if errs, _ := o.CheckPrograms(); len(errs) > 0 {
			problems = problemLines(errs)
		}
	}
	lang := ""
	if len(problems) == 0 {
		if lang, err = s.startLanguage(o); err != nil {
			problems = []string{err.Error()}
		}
	}
	if len(problems) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": strings.Join(problems, "\n"), "problems": problems})
		return
	}
	id, err := s.Chron.NewCampaign(o.Flow, o.Path, o.Start, input, lang)
	if err != nil {
		writeError(w, err)
		return
	}
	release, err := engine.Lock(o, id)
	if err != nil {
		writeError(w, err)
		return
	}
	s.drive(o, id, release)
	writeJSON(w, http.StatusOK, map[string]any{"result": ResumeDriving, "id": id})
}

func problemLines(ps orders.Problems) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// AgainRequest is the body of a retry or rewind request.
type AgainRequest struct {
	Node string `json:"node"` // rewind: the node to go back to
	By   string `json:"by"`   // who asks, when given
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request)  { s.again(w, r, false) }
func (s *Server) handleRewind(w http.ResponseWriter, r *http.Request) { s.again(w, r, true) }

// again retries or rewinds a campaign through the same code as the
// command line, then drives it.
func (s *Server) again(w http.ResponseWriter, r *http.Request, rewind bool) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req AgainRequest
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	o := s.ordersFor(id)
	by := strings.TrimSpace(req.By)
	var release func()
	node := req.Node
	if rewind {
		release, err = engine.Rewind(o, s.Chron, id, node, by, engine.ViaPage)
	} else {
		release, node, err = engine.Retry(o, s.Chron, id, by, engine.ViaPage)
	}
	var refusal *engine.Refusal
	switch {
	case errors.As(err, &refusal):
		writeJSON(w, http.StatusConflict, map[string]string{"error": s.printer(r).Error(err)})
		return
	case err != nil:
		writeError(w, err)
		return
	}
	s.drive(o, id, release)
	writeJSON(w, http.StatusOK, map[string]string{"result": ResumeDriving, "node": node})
}

// OpRequest is the body of a preview or apply request: one operation, and
// for apply the fingerprint of the file the preview was made on.
type OpRequest struct {
	Op   edit.Op `json:"op"`
	Base string  `json:"base"`
	// RunLine is a command as typed, split into Op.Run the way the command
	// line splits -run.
	RunLine string `json:"runLine,omitempty"`
}

// Change is what an operation would do, shown before anything is written.
type Change struct {
	Op       edit.Op  `json:"op"`
	Words    string   `json:"words"` // the operation in plain words
	Error    string   `json:"error,omitempty"`
	Changed  bool     `json:"changed"`
	Diff     string   `json:"diff,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Problems []string `json:"problems,omitempty"` // the changed orders do not pass the check
	Warnings []string `json:"warnings,omitempty"`
	Refused  string   `json:"refused,omitempty"` // a file the page may not write
	Active   []int64  `json:"active,omitempty"`  // campaigns that read the new file when they continue
	Base     string   `json:"base"`
}

// Writable reports whether apply would write the change.
func (c Change) Writable() bool {
	return c.Error == "" && c.Changed && len(c.Problems) == 0 && c.Refused == ""
}

// change makes an operation on an orders file in memory and describes it
// as the command line does: in words, as a diff, and with the problems of
// the orders check.
func (s *Server) change(p *i18n.Printer, f FlowFile, op edit.Op) (*edit.Doc, Change) {
	c := Change{Op: op, Words: i18n.Upper(op.WordsIn(p)), Base: fingerprint(f.path)}
	if op.Op == edit.OpNew {
		c.Error = errNewOnPage.Error()
		return nil, c
	}
	doc, err := edit.Open(f.path)
	if err != nil {
		c.Error = err.Error()
		return nil, c
	}
	if err := doc.Apply(op); err != nil {
		c.Error = err.Error()
		return nil, c
	}
	if c.Changed = doc.Changed(); !c.Changed {
		return doc, c
	}
	c.Diff, c.Notes = doc.Diff(), doc.NotesIn(p)
	o, err := doc.Check()
	var ps orders.Problems
	switch {
	case errors.As(err, &ps):
		c.Problems = problemLines(ps)
	case err != nil:
		c.Problems = []string{err.Error()}
	default:
		c.Warnings = edit.Warnings(o)
	}
	if err := s.mayWrite(f, doc); err != nil {
		c.Refused = err.Error()
	}
	if active := s.active(f.path); len(active) > 0 {
		c.Active = active
	}
	return doc, c
}

// mayWrite keeps the page to the files it may write: the orders file
// itself, starter files an operation creates inside the folder, and the
// prompt templates of the flow's agent nodes that a rename changes. Nothing
// under the folder's .hetman directory and nothing outside the folder.
func (s *Server) mayWrite(f FlowFile, doc *edit.Doc) error {
	dir := filepath.Dir(s.OrdersPath)
	if doc.Path != f.path || !inside(dir, doc.Path) {
		return fmt.Errorf("%s is not an orders file of %s", doc.Path, dir) // i18n: keep (API error)
	}
	templates := map[string]bool{}
	if f.o != nil {
		for _, n := range f.o.Nodes {
			if n.Prompt != "" {
				templates[filepath.Clean(n.Prompt)] = true
			}
		}
	}
	for _, x := range doc.Files() {
		if x.Old != nil && bytes.Equal(x.Old, x.New) {
			continue
		}
		if !inside(dir, x.Path) || x.Old != nil && !templates[filepath.Clean(x.Path)] {
			return fmt.Errorf("the page does not write %s", x.Path) // i18n: keep (API error)
		}
	}
	return nil
}

// inside reports whether path, with symbolic links resolved as far as it
// exists, lies in dir and not in its .hetman directory.
func inside(dir, path string) bool {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	p, rest := filepath.Clean(path), ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
	rel, err := filepath.Rel(realDir, p)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return top != ".hetman"
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	f, req, ok := s.opRequest(w, r)
	if !ok {
		return
	}
	_, c := s.change(s.printer(r), f, req.Op)
	writeJSON(w, http.StatusOK, c)
}

// Results of an apply request.
const (
	ApplyWritten   = "written"   // the change was written
	ApplyUnchanged = "unchanged" // the file already says it; nothing was written
)

// handleApply writes one operation, made again on the file as it is now,
// only if the file is still the one the preview was made on, the result
// passes the orders check and it writes no file the page may not write.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	f, req, ok := s.opRequest(w, r)
	if !ok {
		return
	}
	if req.Base == "" || req.Base != fingerprint(f.path) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errStale.Error(), "stale": "1"})
		return
	}
	doc, c := s.change(s.printer(r), f, req.Op)
	switch {
	case c.Error != "":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": c.Error, "change": c})
		return
	case !c.Changed:
		writeJSON(w, http.StatusOK, map[string]any{"result": ApplyUnchanged, "change": c})
		return
	case len(c.Problems) > 0:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": strings.Join(c.Problems, "\n"), "change": c})
		return
	case c.Refused != "":
		writeJSON(w, http.StatusForbidden, map[string]any{"error": c.Refused, "change": c})
		return
	}
	if err := doc.Save(false); err != nil {
		writeError(w, err)
		return
	}
	var created []string
	for _, x := range doc.Files() {
		if rel, err := filepath.Rel(doc.Dir, x.Path); err == nil {
			created = append(created, filepath.ToSlash(rel))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": ApplyWritten, "change": c, "files": created})
}

func (s *Server) opRequest(w http.ResponseWriter, r *http.Request) (FlowFile, OpRequest, bool) {
	var req OpRequest
	f, err := s.flowFile(r.PathValue("file"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return f, req, false
	}
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return f, req, false
	}
	if strings.TrimSpace(req.RunLine) != "" {
		if req.Op.Run, err = edit.SplitCommand(req.RunLine); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return f, req, false
		}
	}
	return f, req, true
}

// SentenceRequest is the body of a sentence to Scribe.
type SentenceRequest struct {
	Sentence string `json:"sentence"`
}

// SentenceReply is what Scribe made of a sentence. Nothing is written: an
// operation is shown as a change to confirm, an unclear sentence as the
// interpretations to choose from.
type SentenceReply struct {
	Kind    string           `json:"kind"`             // operation, query or unclear
	Change  *Change          `json:"change,omitempty"` // operation
	Words   string           `json:"words,omitempty"`  // query: what was understood
	Text    string           `json:"text,omitempty"`   // query: the answer
	Choices []SentenceChoice `json:"choices,omitempty"`
}

// SentenceChoice is one interpretation of an unclear sentence.
type SentenceChoice struct {
	Question string   `json:"question"`
	Words    string   `json:"words"`
	Op       *edit.Op `json:"op,omitempty"`
	Text     string   `json:"text,omitempty"` // a query: its answer
}

// handleSentence sends one sentence to the model the orders name for
// Scribe, the same way the command line does, and shows what it means.
func (s *Server) handleSentence(w http.ResponseWriter, r *http.Request) {
	f, err := s.flowFile(r.PathValue("file"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	var req SentenceRequest
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p := s.printer(r)
	switch {
	case f.o == nil:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": f.Problem})
		return
	case f.o.Scribe == nil:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": p.T("scribe.no_model", "path", f.path)})
		return
	case strings.TrimSpace(req.Sentence) == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errBadBody.Error()})
		return
	}
	var campaigns []int64
	if list, err := s.Chron.Campaigns(1000); err == nil {
		for i := len(list) - 1; i >= 0; i-- {
			campaigns = append(campaigns, list[i].ID)
		}
	}
	var model scribe.Model = scribe.AdapterModel{Orders: f.o}
	if s.NewModel != nil {
		model = s.NewModel(f.o)
	}
	ans, err := scribe.Interpret(r.Context(), model, req.Sentence, f.o, campaigns)
	switch {
	case errors.Is(err, scribe.ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "invalid": true})
		return
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	reply := SentenceReply{Kind: ans.Outcome}
	switch ans.Outcome {
	case scribe.AnswerOperation:
		_, c := s.change(p, f, *ans.Operation)
		reply.Change = &c
	case scribe.AnswerQuery:
		reply.Words = i18n.Upper(ans.Query.WordsIn(p))
		reply.Text = s.answerQuery(p, *ans.Query)
	default:
		for _, ch := range ans.Choices {
			c := SentenceChoice{Question: strings.TrimSpace(ch.Question), Op: ch.Operation}
			if ch.Operation != nil {
				c.Words = i18n.Upper(ch.Operation.WordsIn(p))
			} else {
				c.Words = i18n.Upper(ch.Query.WordsIn(p))
				c.Text = s.answerQuery(p, *ch.Query)
			}
			reply.Choices = append(reply.Choices, c)
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

// answerQuery explains a campaign from the chronicle in plain words.
func (s *Server) answerQuery(p *i18n.Printer, q scribe.Query) string {
	var k chronicle.Campaign
	var err error
	if q.Campaign == 0 {
		k, err = s.Chron.Latest()
	} else {
		k, err = s.Chron.Campaign(q.Campaign)
	}
	if err != nil {
		return p.T("scribe.no_campaign", "id", q.Campaign)
	}
	events, err := s.Chron.Events(k.ID)
	if err != nil {
		return err.Error()
	}
	return explain.Text(explain.In(p, k, events, q.Kind, q.Node, time.Local))
}
