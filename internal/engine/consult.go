package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/orders"
)

// Request is one request to an executor outside any campaign: the prompt,
// the schema the answer must match, and the model to ask. The model gets no
// tools.
type Request struct {
	Executor string
	Model    string
	Effort   string
	Prompt   string
	Schema   []byte
	Attempt  int
	Timeout  time.Duration
}

// ConsultName is the run and node id an adapter sees for a request.
const ConsultName = "scribe"

// ConsultDir holds the log and the diagnostics of the latest requests.
func ConsultDir(o *orders.Orders) string { return filepath.Join(StateDir(o), ConsultName) }

// Consult runs an executor's adapter for one request under the adapter
// protocol and returns the output of its result. Any other end is an error.
func Consult(ctx context.Context, o *orders.Orders, r Request) (json.RawMessage, error) {
	x := o.Executors[r.Executor]
	if x == nil {
		return nil, fmt.Errorf("executor %q is not declared", r.Executor)
	}
	argv := append([]string{o.Program(x.Run[0])}, x.Run[1:]...)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !contains(contractEnv, strings.ToUpper(name)) {
			env = append(env, kv)
		}
	}
	if _, err := describe(ctx, argv, o.Dir, env); err != nil {
		return nil, fmt.Errorf("executor %s: %v", x.Name, err)
	}
	dir := ConsultDir(o)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := filepath.Join(dir, "request."+strconv.Itoa(r.Attempt))
	logf, err := os.Create(base + ".log")
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	errf, err := os.Create(base + ".stderr.log")
	if err != nil {
		return nil, err
	}
	defer errf.Close()
	if err := os.WriteFile(base+".prompt.txt", []byte(r.Prompt), 0o644); err != nil {
		return nil, err
	}
	start := adapter.EncodeStart(adapter.Step{RunID: ConsultName, NodeID: ConsultName, Attempt: r.Attempt, Prompt: r.Prompt,
		Model: r.Model, Effort: r.Effort, Tools: []string{}, Workdir: o.Dir, Schema: r.Schema})
	run := runAdapter(ctx, adapterProc{argv: argv, dir: o.Dir, env: env, start: start, log: logf, stderr: errf,
		timeout: r.Timeout, started: func(int) {}, session: func(string) {}})
	switch {
	case run.interrupted:
		return nil, ErrInterrupted
	case run.startErr != nil:
		return nil, fmt.Errorf("cannot start %s: %v", x.Run[0], run.startErr)
	case run.timedOut:
		return nil, fmt.Errorf("no answer within %s", r.Timeout)
	case run.protocol != "":
		return nil, fmt.Errorf("the adapter broke the protocol: %s", run.protocol)
	case run.terminal == nil || run.lateExit:
		return nil, fmt.Errorf("the adapter exited with code %d without a result", run.exit)
	case run.exit != 0:
		return nil, fmt.Errorf("the adapter exited with code %d", run.exit)
	case run.terminal.Type == adapter.TypeError:
		return nil, fmt.Errorf("the adapter reported a %s error: %s", run.terminal.Kind, run.terminal.Message)
	}
	return run.terminal.Output, nil
}
