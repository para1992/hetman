package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// Why an executor has no catalogue.
const (
	CatalogNoExecutor  = "no_executor" // the orders declare no such executor
	CatalogUnsupported = "unsupported" // its adapter does not declare the catalog feature
	CatalogFailed      = "failed"      // its adapter could not be asked or gave no valid answer
)

// CatalogResult is what the adapter of an executor says its client offers.
type CatalogResult struct {
	Executor string
	Catalog  *adapter.Catalog // nil when there is none
	Reason   string           // why there is none: CatalogNoExecutor, CatalogUnsupported or CatalogFailed
	Detail   string           // the error behind CatalogFailed, as the system or adapter said it
	At       time.Time        // when the adapter was asked
}

var (
	// catalogTimeout limits one request for a catalogue.
	catalogTimeout = time.Minute
	// catalogFor is how long a catalogue is kept before the adapter is
	// asked again; catalogFailFor the same for an adapter that could not
	// answer, so a page that renders often does not wait on it each time.
	catalogFor     = 10 * time.Minute
	catalogFailFor = 30 * time.Second
	catalogs       = struct {
		sync.Mutex
		m map[string]CatalogResult
	}{m: map[string]CatalogResult{}}
)

// ExecutorCatalog asks the adapter of an executor of the orders what its
// client offers, without a model request. What it said is kept in memory
// for a while; refresh asks again. An executor without a catalogue gives a
// result with Catalog nil and the reason.
func ExecutorCatalog(ctx context.Context, o *orders.Orders, executor string, refresh bool) CatalogResult {
	x := o.Executors[executor]
	if x == nil || len(x.Run) == 0 {
		return CatalogResult{Executor: executor, Reason: CatalogNoExecutor}
	}
	r := CatalogOf(ctx, o.Dir, append([]string{o.Program(x.Run[0])}, x.Run[1:]...), refresh)
	r.Executor = executor
	return r
}

// CatalogOf is ExecutorCatalog for an adapter command run in dir, such as
// one of an executor not written to the orders yet.
func CatalogOf(ctx context.Context, dir string, argv []string, refresh bool) CatalogResult {
	key := dir + "\x00" + strings.Join(argv, "\x00")
	now := time.Now()
	catalogs.Lock()
	r, ok := catalogs.m[key]
	catalogs.Unlock()
	keep := catalogFor
	if r.Catalog == nil {
		keep = catalogFailFor
	}
	if ok && !refresh && now.Sub(r.At) < keep && !now.Before(r.At) {
		return r
	}
	r = askCatalog(ctx, dir, argv)
	r.At = now
	if ctx.Err() == nil {
		catalogs.Lock()
		catalogs.m[key] = r
		catalogs.Unlock()
	}
	return r
}

func askCatalog(ctx context.Context, dir string, argv []string) CatalogResult {
	env := adapterEnv()
	d, err := describe(ctx, argv, dir, env)
	if err != nil {
		return CatalogResult{Reason: CatalogFailed, Detail: err.Error()}
	}
	if !d.Has(adapter.FeatureCatalog) {
		return CatalogResult{Reason: CatalogUnsupported, Detail: fmt.Sprintf("adapter %q does not declare the %s feature", d.Name, adapter.FeatureCatalog)}
	}
	out, err := queryWithin(ctx, argv, []string{adapter.CatalogFlag}, dir, env, catalogTimeout)
	if err != nil {
		return CatalogResult{Reason: CatalogFailed, Detail: err.Error()}
	}
	c, err := adapter.ParseCatalog(out)
	if err != nil {
		return CatalogResult{Reason: CatalogFailed, Detail: err.Error()}
	}
	return CatalogResult{Catalog: &c}
}

// ReasonIn says in the person's language why there is no catalogue.
func (r CatalogResult) ReasonIn(p *i18n.Printer) string {
	switch r.Reason {
	case CatalogNoExecutor:
		return p.T("engine.catalog.no_executor", "executor", r.Executor)
	case CatalogUnsupported:
		return p.T("engine.catalog.unsupported", "executor", r.Executor)
	}
	return p.T("engine.catalog.failed", "executor", r.Executor, "error", r.Detail)
}

// ForgetCatalogs drops every kept catalogue, so the next request asks the
// adapters again.
func ForgetCatalogs() {
	catalogs.Lock()
	catalogs.m = map[string]CatalogResult{}
	catalogs.Unlock()
}

// ClientFinding is what a catalogue says about one model and effort of the
// orders: of an agent node, or of a client of a role.
type ClientFinding struct {
	Node     string // the agent node, or empty for a role's client
	Role     string // the role, for a role's client
	Executor string
	adapter.Finding
}

// Text says the finding in one line, naming where it is.
func (f ClientFinding) Text() string {
	where := fmt.Sprintf("node %q", f.Node)
	if f.Node == "" {
		where = fmt.Sprintf("role %q", f.Role)
	}
	return fmt.Sprintf("%s: %s: %s (catalogue of executor %s)", where, f.Field, f.Finding.Text(), f.Executor)
}

// CatalogFindings checks the model and effort of every agent node and every
// client of a role of the orders (not of their segments) against the
// catalogue of its executor. Executors without a catalogue are skipped.
func CatalogFindings(ctx context.Context, o *orders.Orders) []ClientFinding {
	var out []ClientFinding
	check := func(node, role string, c orders.Client) {
		if c.Executor == "" || o.Executors[c.Executor] == nil {
			return
		}
		r := ExecutorCatalog(ctx, o, c.Executor, false)
		for _, f := range r.Catalog.Check(c.Model, c.Effort) {
			out = append(out, ClientFinding{Node: node, Role: role, Executor: c.Executor, Finding: f})
		}
	}
	for _, id := range o.Order {
		n := o.Nodes[id]
		if n.Type == orders.Agent && n.Role == "" {
			check(id, "", orders.Client{Executor: n.Executor, Model: n.Model, Effort: n.Effort})
		}
	}
	var roles []string
	for name := range o.Roles {
		roles = append(roles, name)
	}
	sort.Strings(roles)
	for _, name := range roles {
		for _, c := range o.Roles[name].Clients {
			check("", name, *c)
		}
	}
	return out
}
