package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// The client's setting closest to effort is the model variant (--variant):
// a provider-specific reasoning setting whose names, such as "low", "high"
// or "max", differ from model to model, and which a model may not have at
// all. The adapter checks the step's effort against the variants the client
// lists for the step's model and rejects a value the model does not offer.

var (
	effortWord = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	modelLine  = regexp.MustCompile(`(?m)^([^\s{}"\[\]]+/\S+)\s*$`)
)

func checkEffort(o options, st adapter.Step, diag io.Writer) error {
	if st.Effort == "" {
		return nil
	}
	if !effortWord.MatchString(st.Effort) {
		return fmt.Errorf("effort %q is not a model variant name such as low, high or max", st.Effort)
	}
	provider, _, ok := strings.Cut(st.Model, "/")
	if !ok || provider == "" {
		fmt.Fprintf(diag, "%s adapter: effort %q is passed as the model variant unchecked: the step names no provider/model\n", adapterName, st.Effort)
		return nil
	}
	variants, found, err := modelVariants(o, st, provider)
	if err != nil || !found {
		fmt.Fprintf(diag, "%s adapter: effort %q is passed as the model variant unchecked: cannot list the variants of %s (%v)\n", adapterName, st.Effort, st.Model, err)
		return nil
	}
	if len(variants) == 0 {
		return fmt.Errorf("effort %q cannot be used: model %s has no variants, the client's only effort setting; remove effort", st.Effort, st.Model)
	}
	for _, v := range variants {
		if v == st.Effort {
			return nil
		}
	}
	return fmt.Errorf("effort %q is not a variant of model %s; its variants: %s", st.Effort, st.Model, strings.Join(variants, ", "))
}

// modelVariants asks the client for its catalogue of the provider's models,
// which needs no model request, and returns the variants of the step's
// model and whether the model is listed.
func modelVariants(o options, st adapter.Step, provider string) ([]string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.client, "models", provider, "--verbose", "--pure")
	cmd.Dir = st.Workdir
	cmd.Env = clientEnv(os.Environ(), o.env, `{"$schema":"https://opencode.ai/config.json"}`, st.Workdir)
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, false, err
	}
	return parseVariants(out.String(), st.Model)
}

// parseVariants returns the variants of a model in the output of
// "models PROVIDER --verbose" and whether the model is listed.
func parseVariants(text, model string) ([]string, bool, error) {
	models, err := parseModels(text)
	if err != nil {
		return nil, false, err
	}
	for _, m := range models {
		if m.id == model {
			return m.variants, true, nil
		}
	}
	return nil, false, nil
}
