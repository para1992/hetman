package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// errChildWaiting is returned by a segment or for-each step whose child
// waits for a person after a subscription limit or lost connectivity: the
// campaign waits too, and resuming it resumes the child.
var errChildWaiting = errors.New("a child campaign waits for a person")

// Item is one entry of a for-each list, as recorded when the step starts.
type Item struct {
	ID    string          `json:"id"`
	Needs []string        `json:"needs,omitempty"`
	Input json.RawMessage `json:"input"` // the whole entry, the input of its child campaign
}

// compositeStep runs a segment or for-each node: its segment as one child
// campaign, or as one child per item. Children are driven by this same
// engine code, each with its own chronicle entries, work folder and
// attempts. A step that was running when the engine stopped continues with
// the children it had started.
func (e *Engine) compositeStep(ctx context.Context, node *orders.Node, attempt int, resumed bool, events []chronicle.Event) error {
	if !resumed {
		ev := chronicle.Event{Kind: chronicle.StepStarted, Node: node.ID, Attempt: attempt}
		problem := ""
		if node.Type == orders.ForEach {
			list, err := e.readItems(node, events)
			if err != nil {
				problem = err.Error()
			} else {
				ev.Items = list
			}
		}
		if err := e.record(ev); err != nil {
			return err
		}
		e.say("%s attempt %d started", node.ID, attempt)
		if problem != "" {
			// The list cannot be run; the step fails with the reason, so the
			// flow can lead back to the step that wrote it.
			e.say("%s failed: %s", node.ID, problem)
			return e.record(chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt, Outcome: orders.Failed,
				Details: chronicle.Details{Error: problem}})
		}
		var err error
		if events, err = e.Chron.Events(e.Campaign); err != nil {
			return err
		}
	}
	if node.Type == orders.Segment {
		return e.runSegment(ctx, node, attempt, events)
	}
	return e.runForEach(ctx, node, attempt, events)
}

// stepStart is the step_started event of an attempt of a node.
func stepStart(events []chronicle.Event, node string, attempt int) *chronicle.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := &events[i]; ev.Kind == chronicle.StepStarted && ev.Node == node && ev.Attempt == attempt {
			return ev
		}
	}
	return nil
}

func (e *Engine) runSegment(ctx context.Context, node *orders.Node, attempt int, events []chronicle.Event) error {
	began := time.Now()
	if s := stepStart(events, node.ID, attempt); s != nil {
		began = s.At
	}
	var child int64
	for _, ev := range events {
		if ev.Kind == chronicle.ChildStarted && ev.Node == node.ID && ev.Attempt == attempt {
			child = ev.Child
		}
	}
	if child == 0 {
		input, err := e.segmentInput(node, events)
		if err != nil {
			e.say("%s failed: %v", node.ID, err)
			return e.record(chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt, Outcome: orders.Failed,
				Details: chronicle.Details{Error: err.Error()}})
		}
		if child, err = e.startChild(node, attempt, "", input); err != nil {
			return err
		}
	}
	res, st, err := e.driveChild(ctx, node, child, "")
	if err != nil {
		return err
	}
	switch res {
	case Done, Failed:
		ms := time.Since(began).Milliseconds()
		e.say("%s finished: %s (campaign %d, %s)", node.ID, res, child, time.Duration(ms)*time.Millisecond)
		return e.record(chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt, Outcome: res.String(),
			Details: chronicle.Details{Child: child, Millis: ms}})
	case Errored:
		return e.stopQuiet(node.ID, fmt.Sprintf("%s: campaign %d stopped in the error state: %s", node.ID, child, st.Error))
	case Waiting:
		return errChildWaiting
	}
	return nil // halted; the halt takes effect in this campaign next
}

// childDone is how the drive of one child of a for-each ended.
type childDone struct {
	item  string
	child int64
	res   Result
	st    chronicle.State
	err   error
}

func (e *Engine) runForEach(ctx context.Context, node *orders.Node, attempt int, events []chronicle.Event) error {
	start := stepStart(events, node.ID, attempt)
	if start == nil {
		return fmt.Errorf("%s attempt %d: no record of its start", node.ID, attempt)
	}
	var list []Item
	if err := json.Unmarshal(start.Items, &list); err != nil {
		return e.stop(node.ID, fmt.Sprintf("%s: the recorded list cannot be read: %v", node.ID, err))
	}
	child := map[string]int64{}
	outcome := map[string]string{}
	notRun := map[string]bool{}
	for _, ev := range events {
		if ev.Seq < start.Seq || ev.Node != node.ID || ev.Attempt != attempt {
			continue
		}
		switch ev.Kind {
		case chronicle.ChildStarted:
			child[ev.Item] = ev.Child
		case chronicle.ItemFinished:
			outcome[ev.Item] = ev.Outcome
		case chronicle.ItemNotRun:
			notRun[ev.Item] = true
		}
	}

	results := make(chan childDone)
	running := map[string]bool{}
	blocked := map[string]childDone{} // errored, or waiting for a person
	var fatal error
	stop, halted := false, false
	for {
		if !stop && e.halting() {
			stop, halted = true, true
		}
		if !stop {
			if err := e.markNotRun(node, attempt, list, outcome, notRun, child); err != nil {
				fatal, stop = err, true
			}
		}
		for _, it := range list {
			if stop || len(running) >= node.Parallel {
				break
			}
			if _, b := blocked[it.ID]; running[it.ID] || b || outcome[it.ID] != "" || notRun[it.ID] {
				continue
			}
			ready := true
			for _, n := range it.Needs {
				ready = ready && outcome[n] == orders.Done
			}
			if !ready {
				continue
			}
			id := child[it.ID]
			if id == 0 {
				var err error
				if id, err = e.startChild(node, attempt, it.ID, it.Input); err != nil {
					fatal, stop = err, true
					break
				}
				child[it.ID] = id
			}
			running[it.ID] = true
			go func(item string, id int64) {
				res, st, err := e.driveChild(ctx, node, id, item)
				results <- childDone{item, id, res, st, err}
			}(it.ID, id)
		}
		if len(running) == 0 {
			break
		}
		r := <-results
		delete(running, r.item)
		switch {
		case errors.Is(r.err, ErrInterrupted):
			stop = true
		case r.err != nil:
			if fatal == nil {
				fatal = r.err
			}
			stop = true
		case r.res == Done || r.res == Failed:
			outcome[r.item] = r.res.String()
			e.say("%s: item %s %s (campaign %d)", node.ID, r.item, r.res, r.child)
			if err := e.record(chronicle.Event{Kind: chronicle.ItemFinished, Node: node.ID, Attempt: attempt, Outcome: r.res.String(),
				Details: chronicle.Details{Item: r.item, Child: r.child}}); err != nil && fatal == nil {
				fatal, stop = err, true
			}
		case r.res == Halted:
			stop, halted = true, true
		default:
			blocked[r.item] = r
		}
	}
	switch {
	case fatal != nil:
		return fatal
	case ctx.Err() != nil:
		return ErrInterrupted
	}
	if err := e.markNotRun(node, attempt, list, outcome, notRun, child); err != nil {
		return err
	}
	left, failed := 0, 0
	for _, it := range list {
		switch {
		case outcome[it.ID] == orders.Done:
		case outcome[it.ID] != "" || notRun[it.ID]:
			failed++
		default:
			left++
		}
	}
	if left > 0 {
		for _, it := range list {
			if b, ok := blocked[it.ID]; ok && b.res == Errored {
				return e.stopQuiet(node.ID, fmt.Sprintf("%s: item %s (campaign %d) stopped in the error state: %s", node.ID, it.ID, b.child, b.st.Error))
			}
		}
		if halted {
			return nil // the halt takes effect in this campaign next
		}
		if len(blocked) > 0 {
			return errChildWaiting
		}
		return e.stop(node.ID, fmt.Sprintf("%s: %d item(s) can neither run nor be skipped", node.ID, left))
	}
	out := orders.Done
	if failed > 0 {
		out = orders.Failed
	}
	ms := time.Since(start.At).Milliseconds()
	e.say("%s finished: %s (%d of %d item(s) done, %s)", node.ID, out, len(list)-failed, len(list), time.Duration(ms)*time.Millisecond)
	return e.record(chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt, Outcome: out,
		Details: chronicle.Details{Millis: ms}})
}

// markNotRun records the items that can no longer run because an item they
// need failed or did not run, until no more are found.
func (e *Engine) markNotRun(node *orders.Node, attempt int, list []Item, outcome map[string]string, notRun map[string]bool, child map[string]int64) error {
	for changed := true; changed; {
		changed = false
		for _, it := range list {
			if outcome[it.ID] != "" || notRun[it.ID] || child[it.ID] != 0 {
				continue
			}
			for _, n := range it.Needs {
				why := ""
				switch {
				case outcome[n] == orders.Failed:
					why = fmt.Sprintf("needs %s, which failed", n)
				case notRun[n]:
					why = fmt.Sprintf("needs %s, which did not run", n)
				default:
					continue
				}
				notRun[it.ID], changed = true, true
				e.say("%s: item %s will not run: %s", node.ID, it.ID, why)
				if err := e.record(chronicle.Event{Kind: chronicle.ItemNotRun, Node: node.ID, Attempt: attempt,
					Details: chronicle.Details{Item: it.ID, Error: why}}); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// startChild creates the child campaign of a segment or of one for-each item
// and records it in this campaign. The child keeps the language for the
// person of this campaign.
func (e *Engine) startChild(node *orders.Node, attempt int, item string, input json.RawMessage) (int64, error) {
	if _, err := e.syncInput(); err != nil {
		return 0, err
	}
	seg := node.Segment
	id, err := e.Chron.NewChild(e.Campaign, node.ID, item, seg.Flow, seg.Path, seg.Start, input, e.lang)
	if err != nil {
		return 0, err
	}
	if err := e.record(chronicle.Event{Kind: chronicle.ChildStarted, Node: node.ID, Attempt: attempt,
		Details: chronicle.Details{Child: id, Item: item}}); err != nil {
		return 0, err
	}
	if item != "" {
		e.say("%s: item %s runs as campaign %d", node.ID, item, id)
	} else {
		e.say("%s runs the segment %s as campaign %d", node.ID, seg.Flow, id)
	}
	return id, nil
}

// driveChild drives a child campaign until it ends, halts, stops in the
// error state or waits for a person. A halt, an error stop or a wait for a
// person left in the child from before is lifted first: this campaign got
// past its own, so it was resumed.
func (e *Engine) driveChild(ctx context.Context, node *orders.Node, child int64, item string) (Result, chronicle.State, error) {
	release, err := Lock(e.top().Orders, child)
	if err != nil {
		return 0, chronicle.State{}, fmt.Errorf("campaign %d: %w", child, err)
	}
	defer release()
	ce := &Engine{Orders: node.Segment, Chron: e.Chron, Campaign: child, Out: e.Out, Clock: e.Clock, parent: e, node: node.ID, item: item}
	events, err := e.Chron.Events(child)
	if err != nil {
		return 0, chronicle.State{}, err
	}
	st := chronicle.Replay(events)
	if st.End == "" && (st.HaltRequested || st.Error != "" || (st.Waiting != "" && st.WaitUntil.IsZero())) && !e.halting() {
		if err := ce.record(chronicle.Event{Kind: chronicle.Resumed, Node: st.Node}); err != nil {
			return 0, st, err
		}
	}
	res, err := ce.Drive(ctx)
	ce.WaitHooks()
	if err != nil {
		return res, st, err
	}
	if events, err = e.Chron.Events(child); err != nil {
		return res, st, err
	}
	return res, chronicle.Replay(events), nil
}

// stopQuiet stops the campaign in the error state because a child did; the
// child's own error already ran the notification hook.
func (e *Engine) stopQuiet(node, msg string) error {
	e.say("error: %s", msg)
	_, err := e.Chron.Append(chronicle.Event{Campaign: e.Campaign, Kind: chronicle.Stopped, Node: node, Details: chronicle.Details{Error: msg}})
	return err
}

// segmentInput is the run input of a segment's child: the input of this
// campaign, or the object the node's input refers to.
func (e *Engine) segmentInput(node *orders.Node, events []chronicle.Event) (json.RawMessage, error) {
	if node.Input == "" {
		if len(events) > 0 {
			return events[0].Input, nil
		}
		return nil, nil
	}
	v, err := e.refValue(events, node.InputRef, node.Input)
	if err != nil {
		return nil, fmt.Errorf("input: %v", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("input: %s must be a JSON object, not %s", node.Input, kindOf(v))
	}
	return json.Marshal(v)
}

// refValue reads the JSON value a reference such as output.plan.items or
// artifact.split.items.json stands for in this campaign now.
func (e *Engine) refValue(events []chronicle.Event, ref orders.Ref, written string) (any, error) {
	var v any
	switch ref.Kind {
	case orders.RefInput:
		v = map[string]any{}
		if len(events) > 0 && len(events[0].Input) > 0 {
			if err := json.Unmarshal(events[0].Input, &v); err != nil {
				return nil, fmt.Errorf("run input: %v", err)
			}
		}
	case orders.RefOutput:
		out := lastOutput(events, ref.Node)
		if out == nil {
			return nil, fmt.Errorf("%s: %s has no accepted output yet", written, ref.Node)
		}
		json.Unmarshal(out, &v)
	case orders.RefArtifact:
		fin := lastFinished(events, ref.Node)
		listed := false
		if fin != nil {
			for _, a := range fin.Artifacts {
				listed = listed || path.Clean(filepath.ToSlash(a)) == path.Clean(ref.File)
			}
		}
		if !listed {
			return nil, fmt.Errorf("%s: the latest run of %s did not list %s as an artifact", written, ref.Node, ref.File)
		}
		data, err := os.ReadFile(filepath.Join(e.dir(), "work", filepath.FromSlash(ref.File)))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", written, err)
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("%s: not valid JSON: %v", written, err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%s: cannot be read as a value", written)
	}
	v, ok := field(v, ref.Path)
	if !ok {
		return nil, fmt.Errorf("%s: no field %q", written, strings.Join(ref.Path, "."))
	}
	return v, nil
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	case string:
		return "a string"
	case bool:
		return "true or false"
	}
	return "a number"
}

// readItems reads the list of a for-each node and checks it: a list of
// objects, each with a unique id and the ids of the items it needs, without
// a cycle.
func (e *Engine) readItems(node *orders.Node, events []chronicle.Event) (json.RawMessage, error) {
	v, err := e.refValue(events, node.ItemsRef, node.Items)
	if err != nil {
		return nil, fmt.Errorf("items %v", err)
	}
	list, err := ParseItems(v)
	if err != nil {
		return nil, fmt.Errorf("items %s: %v", node.Items, err)
	}
	return json.Marshal(list)
}

// ParseItems checks a for-each list and returns its items.
func ParseItems(v any) ([]Item, error) {
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of objects, not %s", kindOf(v))
	}
	var list []Item
	index := map[string]int{}
	for i, x := range raw {
		obj, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d must be an object, not %s", i+1, kindOf(x))
		}
		id, _ := obj[orders.ItemID].(string)
		if !orders.IsName(id) {
			return nil, fmt.Errorf("item %d: %q must be a string of letters, digits, '_', '-' and '.'", i+1, orders.ItemID)
		}
		if j, dup := index[id]; dup {
			return nil, fmt.Errorf("items %d and %d have the same id %q", j+1, i+1, id)
		}
		index[id] = i
		it := Item{ID: id}
		if n, ok := obj[orders.ItemNeeds]; ok && n != nil {
			needs, ok := n.([]any)
			if !ok {
				return nil, fmt.Errorf("item %s: %q must be a list of item ids", id, orders.ItemNeeds)
			}
			for _, d := range needs {
				s, ok := d.(string)
				if !ok {
					return nil, fmt.Errorf("item %s: %q must be a list of item ids", id, orders.ItemNeeds)
				}
				it.Needs = append(it.Needs, s)
			}
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.Encode(obj)
		it.Input = bytes.TrimSpace(b.Bytes())
		list = append(list, it)
	}
	for _, it := range list {
		for _, n := range it.Needs {
			if _, ok := index[n]; !ok {
				return nil, fmt.Errorf("item %s needs %q, which is not in the list", it.ID, n)
			}
		}
	}
	// A cycle is found by walking the needs from every item, keeping the
	// path walked.
	state := map[string]int{} // 1 on the path, 2 done
	var path []string
	var visit func(id string) []string
	visit = func(id string) []string {
		switch state[id] {
		case 1:
			for i, p := range path {
				if p == id {
					return append(append([]string{}, path[i:]...), id)
				}
			}
		case 2:
			return nil
		}
		state[id] = 1
		path = append(path, id)
		for _, n := range list[index[id]].Needs {
			if c := visit(n); c != nil {
				return c
			}
		}
		path = path[:len(path)-1]
		state[id] = 2
		return nil
	}
	for _, it := range list {
		if c := visit(it.ID); c != nil {
			return nil, fmt.Errorf("the items need each other in a cycle: %s", strings.Join(c, " -> "))
		}
	}
	return list, nil
}
