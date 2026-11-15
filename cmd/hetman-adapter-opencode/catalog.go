package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// The catalogue comes from the client's own listing, "models --verbose",
// which needs no model request: every model it can use as a
// "provider/model" line followed by a JSON object with the model's name and
// variants. One listing of every provider gives the variants of every model
// at once (about a second for some 180 models), so the efforts are not
// asked for model by model. The listing names every model the client
// accepts, so the catalogue is complete.

// serveCatalog prints the client's catalogue.
func serveCatalog(o options, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.client, "models", "--verbose", "--pure")
	cmd.Env = clientEnv(os.Environ(), o.env, `{"$schema":"https://opencode.ai/config.json"}`, "")
	cmd.WaitDelay = 2 * time.Second
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "%s adapter: %s models cannot be listed: %v %s\n", adapterName, o.client, err, bytes.TrimSpace(diag.Bytes()))
		return 1
	}
	models, err := parseModels(out.String())
	if err != nil {
		fmt.Fprintf(stderr, "%s adapter: the client's model listing cannot be read: %v\n", adapterName, err)
		return 1
	}
	c := adapter.Catalog{Complete: true, Models: []adapter.CatalogModel{}}
	for _, m := range models {
		c.Models = append(c.Models, adapter.CatalogModel{ID: m.id, Label: m.name, Group: m.provider, Efforts: m.variants})
	}
	data, _ := json.Marshal(c)
	fmt.Fprintf(stdout, "%s\n", data)
	return 0
}

// listedModel is one model of the client's verbose listing.
type listedModel struct {
	id       string // provider/model
	provider string
	name     string
	variants []string // in the client's order; empty when the model has none
}

// parseModels reads the output of "models [PROVIDER] --verbose": each model
// as a "provider/model" line followed by a JSON object.
func parseModels(text string) ([]listedModel, error) {
	locs := modelLine.FindAllStringSubmatchIndex(text, -1)
	var out []listedModel
	for i, l := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		var info struct {
			ProviderID string       `json:"providerID"`
			Name       string       `json:"name"`
			Variants   variantNames `json:"variants"`
		}
		id := text[l[2]:l[3]]
		if err := json.Unmarshal([]byte(text[l[1]:end]), &info); err != nil {
			return nil, fmt.Errorf("model %s: %v", id, err)
		}
		m := listedModel{id: id, provider: info.ProviderID, name: info.Name, variants: append([]string{}, info.Variants...)}
		if m.provider == "" {
			m.provider, _, _ = strings.Cut(id, "/")
		}
		out = append(out, m)
	}
	return out, nil
}

// variantNames are the keys of the variants object, in the order the
// client writes them, which runs from the least effort to the most.
type variantNames []string

func (v *variantNames) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok == nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("variants is not an object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
		*v = append(*v, key.(string))
	}
	return nil
}
