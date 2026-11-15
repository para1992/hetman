package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
			if failed[key] || !d.Has(adapter.FeatureCheck) {
				continue
			}
			steps := byExecutor[name]
			problems, err := checkSteps(ctx, argv, o.Dir, env, steps)
			if err != nil {
				warnings = append(warnings, orders.Problem{File: o.Path, Msg: fmt.Sprintf("warning: executor %q: its adapter could not check the agent nodes: %v", name, err)})
				continue
			}
			for _, p := range problems {
				item := -1
				if p.Item != nil {
					item = *p.Item
				}
				field := p.Field
				if n := o.Nodes[p.NodeID]; n != nil && n.Role != "" && field != adapter.FieldTools {
					field = "role" // the model and effort of a role's client are written under the role
				}
				line, col := orders.Position(o.Path, p.NodeID, field, item)
				msg := fmt.Sprintf("node %q: %s (adapter of executor %s)", p.NodeID, p.Message, name)
				if p.Item != nil {
					for _, st := range steps {
						if st.NodeID == p.NodeID && item >= 0 && item < len(st.Tools) {
							msg = fmt.Sprintf("node %q: tools item %q: %s (adapter of executor %s)", p.NodeID, st.Tools[item], p.Message, name)
						}
					}
				} else if p.Field != "" {
					msg = fmt.Sprintf("node %q: %s: %s (adapter of executor %s)", p.NodeID, p.Field, p.Message, name)
				}
				errs = append(errs, orders.Problem{File: o.Path, Line: line, Col: col, Msg: msg})
			}
		}
	}
	walk(o, 0)
	return errs, warnings
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
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t := time.NewTimer(checkTimeout)
	defer t.Stop()
	select {
	case err := <-exited:
		if err != nil {
			return nil, fmt.Errorf("%s %s failed: %v %s", argv[0], adapter.CheckFlag, err, bytes.TrimSpace(stderr.Bytes()))
		}
	case <-t.C:
		killTree(cmd.Process)
		<-exited
		return nil, fmt.Errorf("%s %s did not finish within %s", argv[0], adapter.CheckFlag, checkTimeout)
	case <-ctx.Done():
		killTree(cmd.Process)
		<-exited
		return nil, errors.New("interrupted")
	}
	return adapter.ParseCheckReply(stdout.Bytes())
}
