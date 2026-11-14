// Package teststep implements the step programs used by tests. A test binary
// re-runs itself as a step, so the same tests work on every system without
// shell scripts.
package teststep

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EnvHelper marks a test binary started as a helper process.
const EnvHelper = "HETMAN_TEST_HELPER"

// TraceFile is appended to by every step, one line per execution:
// "node attempt" or "node.check attempt".
const TraceFile = "trace.txt"

// Main runs the step named by args[0] and returns its exit code.
//
//	exit N              exit with code N
//	outcome NAME [N]    write NAME to the result file, exit with N (default 0)
//	artifact            write work/out.txt and list it in the result file
//	badresult           write a result file that is not JSON
//	env                 write the HETMAN_* variables to work/env.<node>.<attempt>.json
//	spawn TIME          start a grandchild that writes a heartbeat file, then sleep TIME
//	heartbeat FILE      rewrite FILE with a counter every 20ms, forever
//	notice DIR          write standard input to DIR/notice.<HETMAN_REASON>.<node>.json
//	sleep TIME          sleep, then exit with 0
//	block DIR           write DIR/started.<node>.<attempt>, wait for DIR/release,
//	                    then write DIR/exited.<node>.<attempt>
//	orphan DIR          attempt 1: start a grandchild writing DIR/heartbeat, write
//	                    DIR/started.<node>.1 and sleep; later attempts: write
//	                    "dead" or "alive" for that grandchild to DIR/old-grandchild
func Main(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "teststep: no step given")
		return 2
	}
	node, attempt := os.Getenv("HETMAN_NODE_ID"), os.Getenv("HETMAN_ATTEMPT")
	runDir := os.Getenv("HETMAN_RUN_DIR")
	if runDir != "" {
		name := node
		if c := os.Getenv("HETMAN_CHECK_ID"); c != "" {
			name += "." + c
		}
		appendLine(filepath.Join(runDir, TraceFile), name+" "+attempt)
	}
	fmt.Printf("teststep %s for %s attempt %s\n", strings.Join(args, " "), node, attempt)
	switch args[0] {
	case "exit":
		n, _ := strconv.Atoi(arg(args, 1))
		return n
	case "outcome":
		writeResult(map[string]any{"outcome": arg(args, 1)})
		n, _ := strconv.Atoi(arg(args, 2))
		return n
	case "artifact":
		must(os.WriteFile(filepath.Join(runDir, "out.txt"), []byte("built\n"), 0o644))
		writeResult(map[string]any{"artifacts": []string{"out.txt"}})
		return 0
	case "badresult":
		must(os.WriteFile(os.Getenv("HETMAN_RESULT_FILE"), []byte("not json"), 0o644))
		return 0
	case "env":
		vars := map[string]string{}
		for _, kv := range os.Environ() {
			if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "HETMAN_") && k != EnvHelper {
				vars[k] = v
			}
		}
		data, _ := json.Marshal(vars)
		must(os.WriteFile(filepath.Join(runDir, fmt.Sprintf("env.%s.%s.json", node, attempt)), data, 0o644))
		return 0
	case "spawn":
		self, err := os.Executable()
		must(err)
		child := exec.Command(self, "step", "heartbeat", filepath.Join(runDir, "heartbeat"))
		child.Env = os.Environ()
		must(child.Start())
		d, _ := time.ParseDuration(arg(args, 1))
		time.Sleep(d)
		return 0
	case "heartbeat":
		for i := 0; ; i++ {
			os.WriteFile(arg(args, 1), []byte(strconv.Itoa(i)), 0o644)
			time.Sleep(20 * time.Millisecond)
		}
	case "orphan":
		dir := arg(args, 1)
		beat := filepath.Join(dir, "heartbeat")
		if attempt != "1" {
			state := "dead"
			if Beating(beat) {
				state = "alive"
			}
			must(os.WriteFile(filepath.Join(dir, "old-grandchild"), []byte(state), 0o644))
			return 0
		}
		self, err := os.Executable()
		must(err)
		child := exec.Command(self, "step", "heartbeat", beat)
		child.Env = os.Environ()
		must(child.Start())
		must(os.WriteFile(filepath.Join(dir, "started."+node+"."+attempt), nil, 0o644))
		time.Sleep(time.Hour)
		return 0
	case "notice":
		// The notification hook: keep what it was given.
		data, err := io.ReadAll(os.Stdin)
		must(err)
		name := fmt.Sprintf("notice.%s.%s.json", os.Getenv("HETMAN_REASON"), node)
		must(os.WriteFile(filepath.Join(arg(args, 1), name), data, 0o644))
		return 0
	case "sleep":
		d, _ := time.ParseDuration(arg(args, 1))
		time.Sleep(d)
		return 0
	case "block":
		dir := arg(args, 1)
		must(os.WriteFile(filepath.Join(dir, "started."+node+"."+attempt), nil, 0o644))
		for !exists(filepath.Join(dir, "release")) {
			time.Sleep(20 * time.Millisecond)
		}
		must(os.WriteFile(filepath.Join(dir, "exited."+node+"."+attempt), nil, 0o644))
		return 0
	}
	fmt.Fprintf(os.Stderr, "teststep: unknown step %q\n", args[0])
	return 2
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func writeResult(v any) {
	data, _ := json.Marshal(v)
	must(os.WriteFile(os.Getenv("HETMAN_RESULT_FILE"), data, 0o644))
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	must(err)
	defer f.Close()
	fmt.Fprintln(f, line)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "teststep:", err)
		os.Exit(3)
	}
}

// Beating reports whether the heartbeat file at path changes within a
// moment, that is whether the process writing it is alive.
func Beating(path string) bool {
	first, _ := os.ReadFile(path)
	time.Sleep(300 * time.Millisecond)
	second, _ := os.ReadFile(path)
	return string(first) != string(second)
}

// Trace reads the trace file of a run folder.
func Trace(runDir string) []string {
	data, err := os.ReadFile(filepath.Join(runDir, TraceFile))
	if err != nil {
		return nil
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(data)), " ", "#"))
}

// Quote returns s as a single-quoted YAML scalar, so Windows paths survive.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Argv renders a YAML flow list that runs the current test binary as a step.
func Argv(step ...string) string {
	self, err := os.Executable()
	must(err)
	parts := []string{Quote(self), "step"}
	for _, s := range step {
		parts = append(parts, Quote(s))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Adapter renders a YAML flow list that runs the current test binary as the
// stub adapter, with the given arguments (usually a plan file).
func Adapter(args ...string) string {
	self, err := os.Executable()
	must(err)
	parts := []string{Quote(self), "adapter"}
	for _, a := range args {
		parts = append(parts, Quote(a))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
