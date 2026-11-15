package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"hetman/internal/adapter"
)

// The catalogue comes from the client itself: "debug models" prints the
// client's model catalogue as JSON, refreshed from the server for the
// account the client is logged in with (the same list the client offers in
// its model picker, cached in its home folder), without a model request.
// Each model has an id ("slug"), a display name, a visibility ("list" or
// "hide") and the reasoning efforts it supports, which the adapter passes
// to model_reasoning_effort. The server refuses a model the account cannot
// use ("not supported when using Codex with a ChatGPT account"), so this
// catalogue is complete. When it cannot be refreshed, the catalogue bundled
// with the client ("debug models --bundled") is used and marked not
// complete.
//
// One effort is left out: "ultra", which the client describes as maximum
// reasoning with automatic task delegation, delegates to sub-agents, which
// the adapter switches off (see clientArgs).

// delegatingEffort is the effort the adapter refuses.
const delegatingEffort = "ultra"

// listedModel is one model of the client's catalogue.
type listedModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
	Levels      []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// parseModels reads the output of "debug models".
func parseModels(data []byte) ([]listedModel, error) {
	var doc struct {
		Models []listedModel `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Models == nil {
		return nil, fmt.Errorf("no models in the listing")
	}
	return doc.Models, nil
}

// catalogOf turns the client's models into a catalogue.
func catalogOf(models []listedModel, complete bool) adapter.Catalog {
	c := adapter.Catalog{Complete: complete, Models: []adapter.CatalogModel{}}
	seen := map[string]bool{}
	for _, m := range models {
		if m.Slug == "" || seen[m.Slug] {
			continue
		}
		seen[m.Slug] = true
		efforts := []string{}
		dup := map[string]bool{}
		for _, l := range m.Levels {
			if l.Effort != "" && l.Effort != delegatingEffort && !dup[l.Effort] {
				dup[l.Effort] = true
				efforts = append(efforts, l.Effort)
			}
		}
		group := ""
		if m.Visibility == "hide" {
			group = "hidden"
		}
		c.Models = append(c.Models, adapter.CatalogModel{ID: m.Slug, Label: m.DisplayName, Group: group, Efforts: efforts})
	}
	return c
}

// listModels runs "debug models" with the given extra arguments.
func listModels(o options, extra ...string) ([]listedModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.client, append([]string{"debug", "models"}, extra...)...)
	cmd.Env = clientEnv(os.Environ(), o.env)
	cmd.WaitDelay = 2 * time.Second
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s debug models: %v %s", o.client, err, clip(oneLine(diag.String()), 300))
	}
	return parseModels(out.Bytes())
}

// loadCatalog asks the client for its catalogue: the refreshed one, else
// the bundled one, not complete. One adapter process asks once.
func loadCatalog(o options) (adapter.Catalog, error) {
	catalogOnce.Do(func() {
		models, err := listModels(o)
		if err == nil {
			catalogCache = catalogOf(models, true)
			return
		}
		if bundled, berr := listModels(o, "--bundled"); berr == nil {
			catalogCache = catalogOf(bundled, false)
			return
		}
		catalogErr = err
	})
	return catalogCache, catalogErr
}

var (
	catalogOnce  sync.Once
	catalogCache adapter.Catalog
	catalogErr   error
)

// serveCatalog prints the client's catalogue.
func serveCatalog(o options, stdout, stderr io.Writer) int {
	c, err := loadCatalog(o)
	if err != nil {
		fmt.Fprintf(stderr, "%s adapter: the client's models cannot be listed: %v\n", adapterName, err)
		return 1
	}
	data, _ := json.Marshal(c)
	fmt.Fprintf(stdout, "%s\n", data)
	return 0
}

var effortWord = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// effortWordProblem says why an effort cannot be passed at all, or "".
func effortWordProblem(effort string) string {
	switch {
	case effort == "":
		return ""
	case effort == delegatingEffort:
		return fmt.Sprintf("effort %q delegates to sub-agents, which this adapter switches off; use max for the most reasoning", effort)
	case !effortWord.MatchString(effort):
		return fmt.Sprintf("effort %q is not a reasoning effort name such as low, medium, high, xhigh or max", effort)
	}
	return ""
}

// modelProblems judges a step's model and effort by the client's
// catalogue. A model it does not list is a problem when the catalogue is
// complete and a warning otherwise; when it cannot be read, the model is
// said to be unverified and the effort is checked only for its form.
func modelProblems(o options, st adapter.Step, _ io.Writer) []adapter.CheckProblem {
	var out []adapter.CheckProblem
	effort := st.Effort
	if msg := effortWordProblem(effort); msg != "" {
		out = append(out, adapter.CheckProblem{Field: adapter.FieldEffort, Message: msg})
		effort = ""
	}
	if st.Model == "" {
		return out
	}
	c, err := loadCatalog(o)
	if err != nil {
		return append(out, adapter.CheckProblem{Field: adapter.FieldModel, Warning: true, Message: fmt.Sprintf(
			"model %q was not verified: the client's catalogue cannot be read (%v)", st.Model, err)})
	}
	for _, f := range c.Check(st.Model, effort) {
		out = append(out, adapter.CheckProblem{Field: f.Field, Message: f.Text(), Warning: f.Warning})
	}
	return out
}

// checkEffort rejects, before the client runs, an effort the step's model
// does not offer. Without a model, or when the catalogue cannot be read or
// does not list the model, the effort is passed unchecked but for its form.
func checkEffort(o options, st adapter.Step, diag io.Writer) error {
	if st.Effort == "" {
		return nil
	}
	if msg := effortWordProblem(st.Effort); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if st.Model == "" {
		return nil
	}
	c, err := loadCatalog(o)
	if err != nil || c.Model(st.Model) == nil {
		fmt.Fprintf(diag, "%s adapter: effort %q is passed unchecked: model %s is not in the client's catalogue (%v)\n", adapterName, st.Effort, st.Model, err)
		return nil
	}
	for _, f := range c.Check(st.Model, st.Effort) {
		if f.Field == adapter.FieldEffort {
			return fmt.Errorf("%s", f.Text())
		}
	}
	return nil
}
