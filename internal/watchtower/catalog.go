package watchtower

import (
	"net/http"

	"hetman/internal/adapter"
	"hetman/internal/engine"
)

// CatalogView is what the adapter of an executor of an orders file says
// its client offers, for the model and effort fields of the editor.
type CatalogView struct {
	Executor  string `json:"executor"`
	Available bool   `json:"available"`
	// Complete says a model the list does not hold fails at run time; when
	// false the client may accept other names too.
	Complete bool `json:"complete"`
	// Models in the adapter's order. A model's efforts are null when the
	// adapter cannot tell them and an empty list when it has none.
	Models []adapter.CatalogModel `json:"models"`
	Reason string                 `json:"reason,omitempty"` // why there is no catalogue, in the request's language
}

// handleCatalog answers GET /api/flows/{file}/catalog?executor=NAME, with
// refresh=1 to ask the adapter again instead of using what it said lately.
// It asks no model.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	f, err := s.flowFile(r.PathValue("file"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	p := s.printer(r)
	name := r.URL.Query().Get("executor")
	v := CatalogView{Executor: name, Models: []adapter.CatalogModel{}}
	if f.o == nil {
		v.Reason = p.T("page.catalog.invalid_orders")
		writeJSON(w, http.StatusOK, v)
		return
	}
	res := engine.ExecutorCatalog(r.Context(), f.o, name, r.URL.Query().Get("refresh") == "1")
	if res.Catalog == nil {
		v.Reason = res.ReasonIn(p)
		writeJSON(w, http.StatusOK, v)
		return
	}
	v.Available, v.Complete = true, res.Catalog.Complete
	if res.Catalog.Models != nil {
		v.Models = res.Catalog.Models
	}
	writeJSON(w, http.StatusOK, v)
}
