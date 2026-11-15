package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/orders"
)

// checkTimeout limits an adapter's check of the steps of one orders file.
var checkTimeout = 2 * time.Minute

// CheckAdapters asks the adapter of each executor that agent nodes use to
// check the model, effort and tools of those nodes without running them,
// for the orders and every segment. Only an adapter that declares the check
// feature is asked. What it rejects is an error at the line of the orders
// file; an adapter that cannot be asked is a warning, as the orders may be
// checked on another computer than they run on.
func CheckAdapters(ctx context.Context, o *orders.Orders) (errs, warnings orders.Problems) {
	env := adapterEnv()
	described := map[string]adapter.Description{}
	failed := map[string]bool{}
	var walk func(*orders.Orders, int)
	walk = func(o *orders.Orders, depth int) {
		byExecutor := map[string][]adapter.Step{}
		var names []string
		for _, id := range o.Order {
			n := o.Nodes[id]
			if n.Segment != nil && depth < orders.MaxDepth {
				walk(n.Segment, depth+1)
			}
			if n.Type != orders.Agent {
				continue
			}
			// A node run through a role is checked on every client of the role,
			// each with its own model and effort.
			clients := []*orders.Client{{Executor: n.Executor, Model: n.Model, Effort: n.Effort}}
			if r := o.Roles[n.Role]; n.Role != "" && r != nil {
				clients = r.Clients
			}
			for _, c := range clients {
				if c.Executor == "" || o.Executors[c.Executor] == nil {
					continue
				}
				if byExecutor[c.Executor] == nil {
					names = append(names, c.Executor)
				}
				byExecutor[c.Executor] = append(byExecutor[c.Executor], adapter.Step{NodeID: id, Model: c.Model, Effort: c.Effort, Tools: n.Tools, Workdir: n.Workdir})
			}
		}
		for _, name := range names {
			x := o.Executors[name]
			argv := append([]string{o.Program(x.Run[0])}, x.Run[1:]...)
			key := strings.Join(argv, "\x00")
			d, ok := described[key]
			if !ok && !failed[key] {
				var err error
				if d, err = describe(ctx, argv, o.Dir, env); err != nil {
					failed[key] = true
					warnings = append(warnings, orders.Problem{File: o.Path, Msg: fmt.Sprintf("warning: executor %q: its adapter cannot be asked to check the agent nodes: %v", name, err)})
					continue
				}
				described[key] = d
			}
			if failed[key] {
				continue
			}
			if !d.Has(adapter.FeatureProgress) {
				warnings = append(warnings, quietWarnings(o, name, byExecutor[name])...)
			}
			steps := byExecutor[name]
			var items []found
			if d.Has(adapter.FeatureCheck) {
				problems, err := checkSteps(ctx, argv, o.Dir, env, steps)
				if err != nil {
					warnings = append(warnings, orders.Problem{File: o.Path, Msg: fmt.Sprintf("warning: executor %q: its adapter could not check the agent nodes: %v", name, err)})
				}
				for _, p := range problems {
					items = append(items, found{node: p.NodeID, field: p.Field, item: p.Item, msg: p.Message, warning: p.Warning, by: "adapter"})
				}
			}
			if d.Has(adapter.FeatureCatalog) {
				items = withCatalog(items, ExecutorCatalog(ctx, o, name, false).Catalog, steps)
			}
			for _, f := range items {
				item := -1
				if f.item != nil {
					item = *f.item
				}
				field := f.field
				if n := o.Nodes[f.node]; n != nil && n.Role != "" && field != adapter.FieldTools {
					field = "role" // the model and effort of a role's client are written under the role
				}
				line, col := orders.Position(o.Path, f.node, field, item)
				text := f.msg
				if len(f.suggest) > 0 {
					text += "; " + adapter.SuggestText(f.suggest)
				}
				msg := fmt.Sprintf("node %q: %s (%s of executor %s)", f.node, text, f.by, name)
				if f.item != nil {
					for _, st := range steps {
						if st.NodeID == f.node && item >= 0 && item < len(st.Tools) {
							msg = fmt.Sprintf("node %q: tools item %q: %s (%s of executor %s)", f.node, st.Tools[item], text, f.by, name)
						}
					}
				} else if f.field != "" {
					msg = fmt.Sprintf("node %q: %s: %s (%s of executor %s)", f.node, f.field, text, f.by, name)
				}
				if f.warning {
					warnings = append(warnings, orders.Problem{File: o.Path, Line: line, Col: col, Msg: "warning: " + msg})
					continue
				}
				errs = append(errs, orders.Problem{File: o.Path, Line: line, Col: col, Msg: msg})
			}
		}
	}
	walk(o, 0)
	return errs, warnings
}

// found is one problem with a step, from its adapter's check or from the
// catalogue of its executor.
type found struct {
	node, field string
	item        *int
	msg         string
	warning     bool
	suggest     []string
	by          string // "adapter" or "catalogue"
}

// withCatalog adds to what an adapter's check found what the catalogue
// says about the model and effort of each step. Where the adapter already
// reported a problem with the same field of the node, its message stays and
// only the catalogue's suggestions are added to it.
func withCatalog(list []found, c *adapter.Catalog, steps []adapter.Step) []found {
	seen := map[string]bool{}
	for _, st := range steps {
	findings:
		for _, f := range c.Check(st.Model, st.Effort) {
			for i := range list {
				if list[i].node == st.NodeID && list[i].field == f.Field && list[i].item == nil {
					if len(list[i].suggest) == 0 {
						list[i].suggest = f.Suggest
					}
					continue findings
				}
			}
			key := st.NodeID + "\x00" + f.Field + "\x00" + f.Message
			if seen[key] {
				continue
			}
			seen[key] = true
			list = append(list, found{node: st.NodeID, field: f.Field, msg: f.Message, warning: f.Warning, suggest: f.Suggest, by: "catalogue"})
		}
	}
	return list
}

// AdapterWarnings is CheckAdapters for a flow being edited: everything it
// finds, as warnings in the form edit.Warnings gives them, since an edit is
// not refused for what an adapter says.
func AdapterWarnings(ctx context.Context, o *orders.Orders) []string {
	errs, warnings := CheckAdapters(ctx, o)
	var out []string
	for _, p := range append(errs, warnings...) {
		msg := strings.TrimPrefix(p.Msg, "warning: ")
		if p.Line == 0 {
			out = append(out, fmt.Sprintf("%s: %s", filepath.Base(p.File), msg))
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d:%d: %s", filepath.Base(p.File), p.Line, p.Col, msg))
	}
	return out
}

// QuietIdle is the shortest idle_timeout hetman check accepts without a
// warning for a node with an effort whose adapter sends no progress
// messages: a model asked for a high effort may think for many minutes
// without a word, and its client prints nothing meanwhile.
const QuietIdle = 15 * time.Minute

// quietWarnings warns about nodes that set an effort and an idle_timeout
// shorter than QuietIdle on an adapter that reports nothing while the
// model thinks.
func quietWarnings(o *orders.Orders, executor string, steps []adapter.Step) orders.Problems {
	var out orders.Problems
	seen := map[string]bool{}
	for _, st := range steps {
		n := o.Nodes[st.NodeID]
		if seen[st.NodeID] || st.Effort == "" || n == nil || n.IdleTimeout == 0 || n.IdleTimeout >= QuietIdle {
			continue
		}
		seen[st.NodeID] = true
		line, col := orders.Position(o.Path, st.NodeID, "idle_timeout", -1)
		out = append(out, orders.Problem{File: o.Path, Line: line, Col: col, Msg: fmt.Sprintf(
			"warning: node %q: idle_timeout %s with effort %q: the adapter of executor %s sends nothing while the model thinks, which may take longer; use at least %s",
			st.NodeID, n.IdleTimeout, st.Effort, executor, QuietIdle)})
	}
	return out
}

// adapterEnv is the environment an adapter is asked to describe or check
// itself in: the engine's own without the variables of the step contract.
func adapterEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !contains(contractEnv, strings.ToUpper(name)) {
			env = append(env, kv)
		}
	}
	return env
}

// checkSteps runs an adapter with the check flag on steps.
func checkSteps(ctx context.Context, argv []string, dir string, env []string, steps []adapter.Step) ([]adapter.CheckProblem, error) {
	cmd := exec.Command(argv[0], append(append([]string{}, argv[1:]...), adapter.CheckFlag)...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = bytes.NewReader(adapter.EncodeCheck(steps))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s: %v", argv[0], err)
	}
	tree := track(cmd.Process)
	exited := make(chan error, 1)
	go func() { err := cmd.Wait(); tree.end(); exited <- err }()
	t := time.NewTimer(checkTimeout)
	defer t.Stop()
	select {
	case err := <-exited:
		if err != nil {
			return nil, fmt.Errorf("%s %s failed: %v %s", argv[0], adapter.CheckFlag, err, bytes.TrimSpace(stderr.Bytes()))
		}
	case <-t.C:
		tree.kill()
		<-exited
		return nil, fmt.Errorf("%s %s did not finish within %s", argv[0], adapter.CheckFlag, checkTimeout)
	case <-ctx.Done():
		tree.kill()
		<-exited
		return nil, errors.New("interrupted")
	}
	return adapter.ParseCheckReply(stdout.Bytes())
}
