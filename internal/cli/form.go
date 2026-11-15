package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hetman/internal/i18n"
	"hetman/internal/orders"
	"hetman/internal/runinput"
	"hetman/internal/wizard"
)

// inputsDir is the folder under .hetman where inputs given through the
// form are written.
const inputsDir = "inputs"

// writeInput writes a run input given through the form to a new file in
// the inputs folder of a .hetman directory, named after what it is for and
// the time; an existing file is never overwritten.
func writeInput(stateDir, name string, data json.RawMessage) (string, error) {
	dir := filepath.Join(stateDir, inputsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return "", err
	}
	pretty.WriteByte('\n')
	base := name + "-" + time.Now().Format("20060102-150405")
	for i := 1; ; i++ {
		file := base
		if i > 1 {
			file += "-" + strconv.Itoa(i)
		}
		path := filepath.Join(dir, file+".json")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(pretty.Bytes())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return path, err
	}
}

// askInput fills in the run input the orders describe through the form in
// the terminal and writes it to a file under stateDir. It returns the input
// and the file, or an exit code when nothing is to be started.
func askInput(p *i18n.Printer, o *orders.Orders, stateDir, name, command string, stdout, stderr io.Writer) (json.RawMessage, string, int) {
	if o.Input == nil {
		fmt.Fprintln(stderr, p.T("cli.form.no_section", "command", command, "path", o.Path))
		return nil, "", exitUsage
	}
	data, err := wizard.Form(p, o.Input, Stdin, stdout, !isTerminal(Stdin))
	if errors.Is(err, wizard.ErrCancelled) {
		fmt.Fprintln(stdout, p.T("cli.form.cancelled"))
		return nil, "", exitFailed
	}
	if err != nil {
		fmt.Fprintf(stderr, "hetman %s: %v\n", command, err)
		return nil, "", exitFailed
	}
	path, err := writeInput(stateDir, name, data)
	if err != nil {
		fmt.Fprintf(stderr, "hetman %s: %v\n", command, err)
		return nil, "", exitFailed
	}
	fmt.Fprintln(stdout, p.T("cli.form.wrote", "path", path))
	return data, path, exitDone
}

// checkInput checks a run input against the description in the orders, if
// they have one, and returns it with the defaults filled in. Without a
// description the input is returned as it is.
func checkInput(o *orders.Orders, input json.RawMessage) (json.RawMessage, error) {
	if o.Input == nil {
		return input, nil
	}
	out, err := runinput.Check(o.Input, input)
	if err != nil {
		return nil, err
	}
	// No input and nothing filled in stays no input, as without a
	// description.
	if input == nil && string(out) == "{}" {
		return nil, nil
	}
	return out, nil
}
