// Package engine drives a campaign through the nodes of its orders.
//
// The engine owns every transition. Before each move it replays the
// campaign's events from the chronicle, so a restarted engine continues
// from exactly where the previous one stopped.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// Result is how a drive of a campaign ended.
type Result int

const (
	Done    Result = iota // reached the done end
	Failed                // reached the failed end
	Halted                // stopped by a halt
	Errored               // stopped in the error state, waiting for a person
	Waiting               // an agent step hit a subscription limit or lost connectivity; waiting for a person
)

func (r Result) String() string {
	return [...]string{"done", "failed", "halted", "error", "waiting"}[r]
}

// ErrInterrupted is returned when the drive was cancelled during a step. The
// step is left unfinished in the chronicle and runs again on resume.
var ErrInterrupted = errors.New("interrupted")

// ErrLocked is returned when another process is driving the campaign.
var ErrLocked = errors.New("campaign is being run by another process")

// StateDir is the folder next to the orders that holds hetman's files.
func StateDir(o *orders.Orders) string { return filepath.Join(o.Dir, ".hetman") }

// ChroniclePath is the journal file of the orders.
func ChroniclePath(o *orders.Orders) string { return filepath.Join(StateDir(o), "chronicle.db") }

// CampaignDir holds the files of one campaign: work (HETMAN_RUN_DIR), logs,
// results and the driver lock.
func CampaignDir(o *orders.Orders, id int64) string {
	return filepath.Join(StateDir(o), "campaigns", strconv.FormatInt(id, 10))
}

// Lock makes the calling process the only driver of a campaign until the
// returned release is called or the process exits.
//
// Driven takes the same lock for an instant to look at it, so a lock found
// taken is tried again for a moment before ErrLocked is returned; a look
// from status or the web page never makes a real driver give up.
func Lock(o *orders.Orders, id int64) (func(), error) {
	dir := CampaignDir(o, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockPatience)
	for {
		release, err := lockFile(filepath.Join(dir, "lock"))
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lockPatience is how long Lock keeps trying a taken lock.
const lockPatience = 300 * time.Millisecond

// Driven reports whether a process holds the lock of a campaign, that is
// whether the campaign is being driven right now.
func Driven(o *orders.Orders, id int64) (bool, error) {
	path := filepath.Join(CampaignDir(o, id), "lock")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	release, err := lockFile(path)
	if errors.Is(err, ErrLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	release()
	return false, nil
}

// LogFile is the log of one execution of a step or of a check (check is
// empty for a step), relative to the campaign folder, with forward slashes.
func LogFile(node, check string, attempt int) string {
	return "logs/" + execBase(node, check, attempt) + ".log"
}

// ResultFile is the result file of one execution, relative to the campaign
// folder, with forward slashes.
func ResultFile(node, check string, attempt int) string {
	return "results/" + execBase(node, check, attempt) + ".json"
}

func execBase(node, check string, attempt int) string {
	base := node
	if check != "" {
		base += "." + check
	}
	return base + "." + strconv.Itoa(attempt)
}

// Engine drives one campaign.
type Engine struct {
	Orders   *orders.Orders
	Chron    *chronicle.Chronicle
	Campaign int64
	Out      io.Writer // progress lines; nil for none
	Clock    Clock     // nil for the system clock

	described map[string]adapter.Description // per executor, asked once per drive
	waitSaid  time.Time                      // the wait last announced
	saidAsk   int64                          // the question last announced

	outMu sync.Mutex     // progress lines come from the drive and from hooks
	hooks sync.WaitGroup // notification hooks still running

	inputMu sync.Mutex // the input file is synced from the drive and from hooks
	input   []byte     // content of the input file, read from the chronicle once
}

// InputFile is where the run input is written for every command, relative to
// the campaign's work folder (HETMAN_RUN_DIR). A campaign without input gets
// an empty object.
const InputFile = "input.json"

// OutputFile is where the output of the latest accepted attempt of an agent
// node is written for later steps, relative to the campaign's work folder.
func OutputFile(node string) string { return "outputs/" + node + ".json" }

// syncInput makes the input file hold the run input recorded in the
// chronicle, writing it when it is missing or differs, and returns its path.
func (e *Engine) syncInput() (string, error) {
	e.inputMu.Lock()
	defer e.inputMu.Unlock()
	if e.input == nil {
		events, err := e.Chron.Events(e.Campaign)
		if err != nil {
			return "", err
		}
		raw := json.RawMessage("{}")
		if len(events) > 0 && len(events[0].Input) > 0 {
			raw = events[0].Input
		}
		var b bytes.Buffer
		if err := json.Indent(&b, raw, "", "  "); err != nil {
			return "", fmt.Errorf("run input: %v", err)
		}
		b.WriteByte('\n')
		e.input = b.Bytes()
	}
	return syncFile(filepath.Join(CampaignDir(e.Orders, e.Campaign), "work", InputFile), e.input)
}

// syncOutputs writes the output of the latest accepted attempt of every
// agent node to its output file, where it is missing or differs.
func (e *Engine) syncOutputs(events []chronicle.Event) error {
	latest := map[string]json.RawMessage{}
	for _, ev := range events {
		if ev.Kind == chronicle.StepFinished && ev.Outcome != "" && len(ev.Output) > 0 {
			latest[ev.Node] = ev.Output
		}
	}
	work := filepath.Join(CampaignDir(e.Orders, e.Campaign), "work")
	for node, out := range latest {
		var b bytes.Buffer
		if json.Indent(&b, out, "", "  ") != nil {
			continue
		}
		b.WriteByte('\n')
		if _, err := syncFile(filepath.Join(work, filepath.FromSlash(OutputFile(node))), b.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// syncFile writes data to path unless the file already holds exactly that.
func syncFile(path string, data []byte) (string, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

func (e *Engine) say(format string, args ...any) {
	if e.Out != nil {
		e.outMu.Lock()
		defer e.outMu.Unlock()
		fmt.Fprintf(e.Out, "[%d] %s\n", e.Campaign, fmt.Sprintf(format, args...))
	}
}

// record appends an event. An event that makes the campaign wait for a
// person starts the notification hook.
func (e *Engine) record(ev chronicle.Event) error {
	ev.Campaign = e.Campaign
	ev, err := e.Chron.Append(ev)
	if err == nil {
		e.notify(ev)
	}
	return err
}

func (e *Engine) stop(node, msg string) error {
	e.say("error: %s", msg)
	return e.record(chronicle.Event{Kind: chronicle.Stopped, Node: node, Details: chronicle.Details{Error: msg}})
}

// Drive runs the campaign until it ends, halts or stops in the error state.
// The caller must hold the campaign lock.
func (e *Engine) Drive(ctx context.Context) (Result, error) {
	for {
		if ctx.Err() != nil {
			return 0, ErrInterrupted
		}
		events, err := e.Chron.Events(e.Campaign)
		if err != nil {
			return 0, err
		}
		if err := e.syncFiles(events); err != nil {
			return 0, err
		}
		st := chronicle.Replay(events)
		switch {
		case st.End == orders.Done:
			e.say("campaign done")
			return Done, nil
		case st.End == orders.Failed:
			e.say("campaign failed")
			return Failed, nil
		case st.Error != "":
			return Errored, nil
		}
		node := e.Orders.Nodes[st.Node]
		if node == nil {
			if err := e.stop(st.Node, fmt.Sprintf("node %q is not in the orders", st.Node)); err != nil {
				return 0, err
			}
			continue
		}
		if st.HasOutcome {
			if err := e.transition(node, st.Outcome); err != nil {
				return 0, err
			}
			continue
		}
		if st.HaltRequested {
			if !st.Halted {
				if err := e.record(chronicle.Event{Kind: chronicle.Halted, Node: node.ID}); err != nil {
					return 0, err
				}
			}
			e.say("halted before %s", node.ID)
			return Halted, nil
		}
		if st.Ask != nil {
			switch {
			case st.Answer == nil:
				err = e.awaitAnswer(ctx, st.Ask)
			case st.Ask.Reason != chronicle.ReasonDialogue:
				err = e.finishAsked(events, st)
			}
			if err != nil {
				return 0, err
			}
			if st.Answer == nil || st.Ask.Reason != chronicle.ReasonDialogue {
				continue
			}
		}
		if st.Waiting != "" {
			if st.WaitUntil.IsZero() {
				what := "the subscription limit was reached"
				if st.Waiting == chronicle.WaitOffline {
					what = "connectivity was lost"
				}
				e.say("%s waits for a person: %s; hetman resume runs it again", node.ID, what)
				return Waiting, nil
			}
			if e.clock().Now().Before(st.WaitUntil) {
				if err := e.wait(ctx, node.ID, st.WaitUntil); err != nil {
					return 0, err
				}
				continue
			}
		}
		if st.Running {
			e.say("%s attempt %d was interrupted; running it again", node.ID, st.Attempt)
			if err := e.cleanup(events, node.ID, st.Attempt); err != nil {
				return 0, err
			}
			if err := e.record(chronicle.Event{Kind: chronicle.StepInterrupted, Node: node.ID, Attempt: st.Attempt}); err != nil {
				return 0, err
			}
		}
		switch node.Type {
		case orders.Agent:
			err = e.agentStep(ctx, node, st.Attempts[node.ID]+1, st, events)
		case orders.Human:
			err = e.humanStep(node, st.Attempts[node.ID]+1)
		default:
			err = e.step(ctx, node, st.Attempts[node.ID]+1)
		}
		if err != nil {
			return 0, err
		}
	}
}

// syncFiles restores the files the engine keeps in the work folder from the
// chronicle: the run input and the outputs of agent nodes.
func (e *Engine) syncFiles(events []chronicle.Event) error {
	if _, err := e.syncInput(); err != nil {
		return err
	}
	return e.syncOutputs(events)
}

// cleanup makes sure no process of an interrupted attempt still runs before
// the step runs again. Only the last process the attempt started can still
// be running; anything it left behind belongs to its process tree.
func (e *Engine) cleanup(events []chronicle.Event, node string, attempt int) error {
	var last *chronicle.Event
	for i := range events {
		if ev := &events[i]; ev.Kind == chronicle.ProcessStarted && ev.Node == node && ev.Attempt == attempt {
			last = ev
		}
	}
	if last == nil {
		// The engine died after starting the attempt but before the
		// process_started event was committed, so there is no process id to
		// check. On Linux the kernel and on Windows the job object have
		// killed the step with the engine; elsewhere its tree is unknown.
		e.say("no process of %s attempt %d was recorded; nothing to check", node, attempt)
		return e.record(chronicle.Event{Kind: chronicle.ProcessCleanup, Node: node, Attempt: attempt, Outcome: cleanupUnrecorded})
	}
	result := cleanupStale(last.Pid, last.Ident)
	e.say("process %d of %s attempt %d: %s", last.Pid, node, attempt, result)
	return e.record(chronicle.Event{Kind: chronicle.ProcessCleanup, Node: node, Check: last.Check, Attempt: attempt,
		Outcome: result, Details: chronicle.Details{Pid: last.Pid, Ident: last.Ident}})
}

func (e *Engine) transition(node *orders.Node, outcome string) error {
	target, ok := node.On[outcome]
	if !ok {
		return e.stop(node.ID, fmt.Sprintf("node %q produced outcome %q, which has no transition", node.ID, outcome))
	}
	e.say("%s -> %s", node.ID, target)
	return e.record(chronicle.Event{Kind: chronicle.Transition, Node: node.ID, Outcome: outcome, Details: chronicle.Details{To: target}})
}

func (e *Engine) step(ctx context.Context, node *orders.Node, attempt int) error {
	dir := CampaignDir(e.Orders, e.Campaign)
	for _, sub := range []string{"work", "logs", "results"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := e.record(chronicle.Event{Kind: chronicle.StepStarted, Node: node.ID, Attempt: attempt}); err != nil {
		return err
	}
	e.say("%s attempt %d started", node.ID, attempt)
	began := time.Now()
	fin := chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt}
	if len(node.Checks) > 0 || len(node.Ask) > 0 {
		outcome, msg, err := e.gate(ctx, node, attempt)
		if err != nil {
			return err
		}
		fin.Outcome, fin.Error = outcome, msg
	} else {
		okName, failName := orders.OK, orders.Fail
		if node.Type == orders.Gate {
			okName, failName = orders.Pass, orders.Blocked
		}
		r := e.exec(ctx, node.ID, "", node.Run, node.Timeout, attempt, okName, failName)
		if r.interrupted {
			return ErrInterrupted
		}
		fin.Outcome, fin.Error, fin.Exit, fin.Log, fin.Artifacts = r.outcome, r.err, r.exit, r.log, r.artifacts
	}
	fin.Millis = time.Since(began).Milliseconds()
	if len(node.Ask) > 0 && fin.Outcome == orders.Pass && fin.Error == "" {
		// The checks let the gate pass, so the person is asked; the gate
		// finishes once they have answered.
		return e.record(chronicle.Event{Kind: chronicle.Asked, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
			Reason: chronicle.ReasonGate, Questions: node.Ask, Show: node.Show}})
	}
	if err := e.record(fin); err != nil {
		return err
	}
	if fin.Error != "" {
		return e.stop(node.ID, fmt.Sprintf("%s attempt %d: %s", node.ID, attempt, fin.Error))
	}
	e.say("%s finished: %s (%s)", node.ID, fin.Outcome, time.Duration(fin.Millis)*time.Millisecond)
	return nil
}

// gate runs the gate's checks in order. A failed required check blocks the
// gate and skips the rest; a failed soft check is only a warning.
func (e *Engine) gate(ctx context.Context, node *orders.Node, attempt int) (outcome, errMsg string, err error) {
	for _, name := range node.Checks {
		c := e.Orders.Checks[name]
		began := time.Now()
		r := e.exec(ctx, node.ID, name, c.Run, c.Timeout, attempt, orders.OK, orders.Fail)
		if r.interrupted {
			return "", "", ErrInterrupted
		}
		ev := chronicle.Event{Kind: chronicle.CheckFinished, Node: node.ID, Check: name, Attempt: attempt, Outcome: r.outcome,
			Details: chronicle.Details{Error: r.err, Exit: r.exit, Log: r.log, Artifacts: r.artifacts, Soft: c.Soft,
				Millis: time.Since(began).Milliseconds()}}
		if err := e.record(ev); err != nil {
			return "", "", err
		}
		if r.err != "" {
			return "", fmt.Sprintf("check %s: %s", name, r.err), nil
		}
		switch {
		case r.outcome == orders.OK:
			e.say("  check %s: ok", name)
		case c.Soft:
			e.say("  check %s: %s (soft, warning)", name, r.outcome)
		default:
			e.say("  check %s: %s (required, gate blocked)", name, r.outcome)
			return orders.Blocked, "", nil
		}
	}
	return orders.Pass, "", nil
}

type execResult struct {
	outcome     string
	err         string
	exit        *int
	log         string
	artifacts   []string
	interrupted bool
}

// contractEnv are the variables the engine sets; inherited values are dropped.
var contractEnv = []string{"HETMAN_RUN_ID", "HETMAN_NODE_ID", "HETMAN_CHECK_ID", "HETMAN_ATTEMPT", "HETMAN_RUN_DIR", "HETMAN_RESULT_FILE", "HETMAN_INPUT_FILE", "HETMAN_REASON"}

// exec runs one command under the script contract and maps its exit to an
// outcome: okName for exit code 0, failName otherwise, timeout when killed
// for exceeding the timeout, or the name written to the result file.
func (e *Engine) exec(ctx context.Context, nodeID, check string, argv []string, timeout time.Duration, attempt int, okName, failName string) execResult {
	dir := CampaignDir(e.Orders, e.Campaign)
	var r execResult
	r.log = LogFile(nodeID, check, attempt)
	resultFile := filepath.Join(dir, filepath.FromSlash(ResultFile(nodeID, check, attempt)))
	if err := os.Remove(resultFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.err = err.Error()
		return r
	}
	env, err := e.env(nodeID, check, attempt, resultFile)
	if err != nil {
		r.err = err.Error()
		return r
	}
	log, err := os.Create(filepath.Join(dir, r.log))
	if err != nil {
		r.err = err.Error()
		return r
	}
	defer log.Close()
	cmd := append([]string{e.Orders.Program(argv[0])}, argv[1:]...)
	started := func(pid int) {
		ident, _ := processIdent(pid)
		ev := chronicle.Event{Kind: chronicle.ProcessStarted, Node: nodeID, Check: check, Attempt: attempt,
			Details: chronicle.Details{Pid: pid, Ident: ident}}
		if err := e.record(ev); err != nil {
			e.say("cannot record process %d of %s: %v", pid, nodeID, err)
			fmt.Fprintln(log, "hetman: cannot record the process:", err)
		}
	}
	pr := runProcess(ctx, cmd, e.Orders.Dir, env, log, timeout, started)
	switch {
	case pr.interrupted:
		r.interrupted = true
		return r
	case pr.startErr != nil:
		r.err = fmt.Sprintf("cannot start %s: %v", argv[0], pr.startErr)
		fmt.Fprintln(log, "hetman:", r.err)
		return r
	}
	exit := pr.exit
	r.exit = &exit
	if pr.timedOut {
		r.outcome = orders.Timeout
		fmt.Fprintf(log, "\nhetman: killed after timeout %s\n", timeout)
		return r
	}
	r.outcome = okName
	if exit != 0 {
		r.outcome = failName
	}
	override, artifacts, err := readResult(resultFile)
	if err != nil {
		r.outcome, r.err = "", err.Error()
		return r
	}
	if override != "" {
		r.outcome = override
	}
	r.artifacts = artifacts
	return r
}

// env is the environment of a step process: the engine's own without any
// HETMAN_* variable of the contract, plus those that apply. An empty node
// gives only the campaign variables, an empty result file leaves out
// HETMAN_RESULT_FILE. The input file is synced first, so every command finds
// the run input there whatever happened to the file before.
func (e *Engine) env(nodeID, check string, attempt int, resultFile string) ([]string, error) {
	input, err := e.syncInput()
	if err != nil {
		return nil, err
	}
	env := make([]string, 0, len(os.Environ())+len(contractEnv))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !contains(contractEnv, strings.ToUpper(name)) {
			env = append(env, kv)
		}
	}
	env = append(env,
		"HETMAN_RUN_ID="+strconv.FormatInt(e.Campaign, 10),
		"HETMAN_RUN_DIR="+filepath.Join(CampaignDir(e.Orders, e.Campaign), "work"),
		"HETMAN_INPUT_FILE="+input,
	)
	if nodeID != "" {
		env = append(env, "HETMAN_NODE_ID="+nodeID, "HETMAN_ATTEMPT="+strconv.Itoa(attempt))
	}
	if resultFile != "" {
		env = append(env, "HETMAN_RESULT_FILE="+resultFile)
	}
	if check != "" {
		env = append(env, "HETMAN_CHECK_ID="+check)
	}
	return env, nil
}

func readResult(path string) (string, []string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var res struct {
		Outcome   *string  `json:"outcome"`
		Artifacts []string `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", nil, fmt.Errorf("result file is not valid JSON: %v", err)
	}
	if res.Outcome == nil {
		return "", res.Artifacts, nil
	}
	if *res.Outcome == "" || strings.ContainsAny(*res.Outcome, " \t\r\n") {
		return "", nil, fmt.Errorf("result file: invalid outcome %q", *res.Outcome)
	}
	return *res.Outcome, res.Artifacts, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
