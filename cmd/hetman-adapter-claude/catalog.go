package main

import (
	"encoding/json"
	"fmt"
	"io"

	"hetman/internal/adapter"
)

// The client has no command that lists its models, so the catalogue is the
// adapter's own table: the model aliases the client's help names (see
// modelAliases), each with the effort levels the adapter passes on (see
// efforts). The client also accepts a model's full name, which the table
// cannot list, so the catalogue is not complete.
var catalogue = adapter.Catalog{Complete: false, Models: []adapter.CatalogModel{
	{ID: "haiku", Label: "Haiku, the latest", Efforts: efforts},
	{ID: "sonnet", Label: "Sonnet, the latest", Efforts: efforts},
	{ID: "opus", Label: "Opus, the latest", Efforts: efforts},
	{ID: "fable", Label: "Fable, the latest", Efforts: efforts},
}}

// serveCatalog prints the adapter's catalogue.
func serveCatalog(stdout io.Writer) int {
	data, _ := json.Marshal(catalogue)
	fmt.Fprintf(stdout, "%s\n", data)
	return 0
}
