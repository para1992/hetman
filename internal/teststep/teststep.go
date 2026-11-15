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
//	input [MODE]        copy the file named by HETMAN_INPUT_FILE to
//	                    work/seen.<node>[.<check>].<attempt>.json; MODE "tamper"
//	                    then overwrites that file, "delete" removes it
//	spawn TIME          start a grandchild that writes a heartbeat file, then sleep TIME
//	heartbeat FILE      rewrite FILE with a counter every 20ms, forever
//	notice DIR          write standard input to DIR/notice.<HETMAN_REASON>.<node>.json
//	sleep TIME          sleep, then exit with 0
//	block DIR           write DIR/started.<node>.<attempt>, wait for DIR/release,
//	                    then write DIR/exited.<node>.<attempt>
//	write FILE TEXT     write TEXT to work/FILE and list it as an artifact
//	item DIR            act on the run input of a for-each item {"id", "sleep",
//	                    "wait", "fail", "failonce"}: write DIR/started.<id>.<run id>,
//	                    wait for DIR/release when "wait" is true, sleep "sleep",
//	                    then exit 1 when "fail" is true, or when "failonce" is true
//	                    and DIR/failed.<id> does not exist yet (it is written),
//	                    else write DIR/finished.<id>
//	                    An item with "meet": GROUP and "meetn": N first writes
//	                    DIR/met.<GROUP>.<id> and waits until N items of the group
//	                    have, so that they run together by construction; it exits
//	                    1 when they do not within 30s.
//	meet DIR N          write DIR/met.all.<run id> and wait until N steps have
//	                    (DIR/met.all.*), then exit 0; exit 1 after 30s
//	failonce DIR        exit 1 the first time for this node (DIR/failed.<node>), then 0
//	badonce DIR         write a result file that is not JSON the first time for
//	                    this node (DIR/bad.<node>), then exit 0
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
	case "input":
		name := node
		if c := os.Getenv("HETMAN_CHECK_ID"); c != "" {
			name += "." + c
		}
		path := os.Getenv("HETMAN_INPUT_FILE")
		data, err := os.ReadFile(path)
		must(err)
		must(os.WriteFile(filepath.Join(runDir, fmt.Sprintf("seen.%s.%s.json", name, attempt)), data, 0o644))
		switch arg(args, 1) {
		case "tamper":
			must(os.WriteFile(path, []byte(`{"tampered": true}`), 0o644))
		case "delete":
			must(os.Remove(path))
		}
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
	case "write":
		must(os.WriteFile(filepath.Join(runDir, arg(args, 1)), []byte(arg(args, 2)), 0o644))
		writeResult(map[string]any{"artifacts": []string{arg(args, 1)}})
		return 0
	case "writeexit": // writeexit FILE TEXT CODE: write FILE, list it as an artifact, exit with CODE
		must(os.WriteFile(filepath.Join(runDir, arg(args, 1)), []byte(arg(args, 2)), 0o644))
		writeResult(map[string]any{"artifacts": []string{arg(args, 1)}})
		n, _ := strconv.Atoi(arg(args, 3))
		return n
	case "item":
		dir := arg(args, 1)
		data, err := os.ReadFile(os.Getenv("HETMAN_INPUT_FILE"))
		must(err)
		var in struct {
			ID    string `json:"id"`
			Sleep string `json:"sleep"`
			Wait  bool   `json:"wait"`
			Fail  bool   `json:"fail"`
			Once  bool   `json:"failonce"`
			Meet  string `json:"meet"`
			MeetN int    `json:"meetn"`
		}
		must(json.Unmarshal(data, &in))
		must(os.WriteFile(filepath.Join(dir, "started."+in.ID+"."+os.Getenv("HETMAN_RUN_ID")), nil, 0o644))
		if in.Meet != "" && !meet(dir, in.Meet, in.ID, in.MeetN) {
			return 1
		}
		for in.Wait && !exists(filepath.Join(dir, "release")) {
			time.Sleep(20 * time.Millisecond)
		}
		if d, err := time.ParseDuration(in.Sleep); err == nil {
			time.Sleep(d)
		}
		if in.Fail {
			return 1
		}
		if in.Once && !exists(filepath.Join(dir, "failed."+in.ID)) {
			must(os.WriteFile(filepath.Join(dir, "failed."+in.ID), nil, 0o644))
			return 1
		}
		must(os.WriteFile(filepath.Join(dir, "finished."+in.ID), nil, 0o644))
		return 0
	case "meet":
		n, _ := strconv.Atoi(arg(args, 2))
		if !meet(arg(args, 1), "all", os.Getenv("HETMAN_RUN_ID"), n) {
			return 1
		}
		return 0
	case "failonce":
		mark := filepath.Join(arg(args, 1), "failed."+node)
		if !exists(mark) {
			must(os.WriteFile(mark, nil, 0o644))
			return 1
		}
		return 0
	case "badonce":
		mark := filepath.Join(arg(args, 1), "bad."+node)
		if !exists(mark) {
			must(os.WriteFile(mark, nil, 0o644))
			must(os.WriteFile(os.Getenv("HETMAN_RESULT_FILE"), []byte("not json"), 0o644))
		}
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

// meet writes DIR/met.<group>.<id> and waits until n such files of the
// group exist; it reports false when they do not within 30 seconds.
func meet(dir, group, id string, n int) bool {
	must(os.WriteFile(filepath.Join(dir, "met."+group+"."+id), nil, 0o644))
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if met, _ := filepath.Glob(filepath.Join(dir, "met."+group+".*")); len(met) >= n {
			return true
		}
	}
	fmt.Fprintf(os.Stderr, "teststep: fewer than %d steps of %s ran together\n", n, group)
	return false
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
