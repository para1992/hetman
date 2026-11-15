// Package watchtower serves the web page that shows the campaigns of one
// orders file live, and lets a person halt and resume them and answer the
// questions a campaign asks.
//
// Everything shown is derived from the chronicle, as the command line does.
// The page is kept current with server-sent events; changes are noticed by
// a cheap look at the chronicle at a short interval, made only while at
// least one page is connected.
package watchtower

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/dispatch"
	"hetman/internal/engine"
	"hetman/internal/explain"
	"hetman/internal/orders"
)

//go:embed assets
var assets embed.FS

// ControlHeader must be present with value "1" on halt and resume requests.
// A form on another web site cannot send it, and a script on another site
// cannot send it without a preflight request this server never approves.
const ControlHeader = "X-Hetman-Control"

// listLimit is how many campaigns the list shows, newest first.
const listLimit = 200

// Server serves Watchtower for the orders file at OrdersPath.
type Server struct {
	OrdersPath string
	Chron      *chronicle.Chronicle
	// Poll is how often the chronicle is looked at while a page is
	// connected; zero means 250ms.
	Poll time.Duration
	// Hosts are host names accepted in the Host header besides localhost
	// and IP addresses, which keeps other sites out by DNS rebinding.
	Hosts []string
	// Log receives the progress lines of campaigns this server drives.
	Log io.Writer

	ctx    context.Context
	drives sync.WaitGroup

	mu      sync.Mutex
	orders  *orders.Orders
	stamp   time.Time
	size    int64
	problem string
	others  map[string]loaded // other orders files of the folder, by path
	tasks   *dispatch.Queue   // the task queue of the folder, once it exists

	hub hub
}

// New returns a server for orders that were already loaded from their file.
// Campaigns resumed from the page are driven until ctx is cancelled.
func New(ctx context.Context, o *orders.Orders, ch *chronicle.Chronicle) *Server {
	s := &Server{OrdersPath: o.Path, Chron: ch, ctx: ctx, orders: o}
	if fi, err := os.Stat(o.Path); err == nil {
		s.stamp, s.size = fi.ModTime(), fi.Size()
	}
	return s
}

// Wait waits until the campaigns driven by this server have stopped, which
// they do once the context given to New is cancelled.
func (s *Server) Wait() { s.drives.Wait() }

func (s *Server) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 250 * time.Millisecond
}

// currentOrders returns the orders, read again when the file has changed.
// While the file does not load, the last good orders are kept and the
// problem is reported on the page.
func (s *Server) currentOrders() (*orders.Orders, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(s.OrdersPath)
	if err != nil {
		return s.orders, err.Error()
	}
	if fi.ModTime().Equal(s.stamp) && fi.Size() == s.size {
		return s.orders, s.problem
	}
	s.stamp, s.size = fi.ModTime(), fi.Size()
	o, err := orders.Load(s.OrdersPath)
	if err != nil {
		s.problem = "the orders file does not load, so the last good version is shown: " + err.Error()
		return s.orders, s.problem
	}
	s.orders, s.problem = o, ""
	return o, ""
}

// Handler returns the HTTP handler of the page and its API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "assets")
	files := http.FileServerFS(static)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", noCache(files)))
	mux.HandleFunc("GET /api/campaigns", s.handleList)
	mux.HandleFunc("GET /api/campaigns/{id}", s.handleCampaign)
	mux.HandleFunc("GET /api/campaigns/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/campaigns/{id}/file", s.handleFile)
	mux.HandleFunc("GET /api/campaigns/{id}/log", s.handleLog)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.Handle("POST /api/campaigns/{id}/halt", s.control(s.handleHalt))
	mux.Handle("POST /api/campaigns/{id}/resume", s.control(s.handleResume))
	mux.HandleFunc("GET /api/campaigns/{id}/show", s.handleShow)
	mux.Handle("POST /api/campaigns/{id}/answer", s.control(s.handleAnswer))
	return s.guard(mux)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// guard refuses requests addressed to a host name other than a known one
// and sets the headers that keep the page from loading anything foreign.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "unknown host "+strconv.Quote(r.Host), http.StatusMisdirectedRequest)
			return
		}
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil {
		return true
	}
	for _, h := range s.Hosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

// control protects a state-changing handler against requests sent by other
// web sites open in the same browser: the browser's own Origin and
// Sec-Fetch-Site headers must name this server, and ControlHeader must be
// set, which a plain cross-site form or link cannot do.
func (s *Server) control(h http.HandlerFunc) http.Handler {
	cop := http.NewCrossOriginProtection()
	return cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ControlHeader) != "1" {
			http.Error(w, "missing "+ControlHeader+" header", http.StatusForbidden)
			return
		}
		h(w, r)
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, chronicle.ErrNoCampaign), errors.Is(err, errBadID):
		status = http.StatusNotFound
	case errors.Is(err, engine.ErrEnded), errors.Is(err, engine.ErrNothingAsked), errors.Is(err, engine.ErrAnswered),
		errors.Is(err, engine.ErrOtherAsk):
		status = http.StatusConflict
	case errors.Is(err, ask.ErrInvalid), errors.Is(err, errBadAnswer):
		status = http.StatusBadRequest
	case errors.Is(err, errBadPath):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

var (
	errBadID     = errors.New("no such campaign")
	errBadPath   = errors.New("path must name a file under logs, results, prompts or work of the campaign")
	errBadAnswer = errors.New("the answer must be a JSON object")
)

func campaignID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errBadID
	}
	return id, nil
}

// summaries returns the rows of the campaign list.
func (s *Server) summaries() ([]Summary, error) {
	list, err := s.Chron.Campaigns(listLimit)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(list))
	for _, k := range list {
		events, err := s.Chron.Events(k.ID)
		if err != nil {
			return nil, err
		}
		o, _ := s.ordersOf(k)
		out = append(out, s.build(o, k, events).Summary)
	}
	return out, nil
}

// campaign returns the view of one campaign and its events.
func (s *Server) campaign(id int64) (*Campaign, []chronicle.Event, error) {
	k, err := s.Chron.Campaign(id)
	if err != nil {
		return nil, nil, err
	}
	events, err := s.Chron.Events(id)
	if err != nil {
		return nil, nil, err
	}
	o, problem := s.ordersOf(k)
	c := s.build(o, k, events)
	c.OrdersProblem = problem
	c.Words = explain.Campaign(k, events, explain.Summary, "", time.Local)
	return c, events, nil
}

func (s *Server) build(o *orders.Orders, k chronicle.Campaign, events []chronicle.Event) *Campaign {
	driven := func() bool {
		d, _ := engine.Driven(o, k.ID)
		return d
	}
	return build(k, events, o, engine.CampaignDir(o, k.ID), driven)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.summaries()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCampaign(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	c, _, err := s.campaign(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.Chron.Campaign(id); err != nil {
		writeError(w, err)
		return
	}
	events, err := s.Chron.Events(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eventsJSON(events))
}

// campaignFile resolves a path relative to a campaign folder, allowing only
// files under logs, results and work, even through symbolic links.
func (s *Server) campaignFile(id int64, rel string) (string, error) {
	local := filepath.FromSlash(rel)
	if rel == "" || !filepath.IsLocal(local) {
		return "", errBadPath
	}
	top, _, _ := strings.Cut(filepath.ToSlash(filepath.Clean(local)), "/")
	if top != "logs" && top != "results" && top != "work" && top != "prompts" {
		return "", errBadPath
	}
	o, _ := s.currentOrders()
	dir := engine.CampaignDir(o, id)
	full := filepath.Join(dir, local)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return full, nil // nothing exists yet; the caller finds no file
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return full, nil
	}
	if r, err := filepath.Rel(realDir, real); err != nil || !filepath.IsLocal(r) {
		return "", errBadPath
	}
	return real, nil
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.Chron.Campaign(id); err != nil {
		writeError(w, err)
		return
	}
	path, err := s.campaignFile(id, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "no such file", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not a file", http.StatusNotFound)
		return
	}
	// Files are always shown as text, never run as a page of this site.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleHalt(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	o, _ := s.currentOrders()
	result, _, err := engine.Halt(o, s.Chron, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

// Results of a resume request.
const (
	ResumeDriving   = "driving"   // this server now drives the campaign
	ResumeElsewhere = "elsewhere" // another process drives it and continues
)

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	o := s.ordersFor(id)
	release, _, err := engine.Resume(o, s.Chron, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if release == nil {
		writeJSON(w, http.StatusOK, map[string]string{"result": ResumeElsewhere})
		return
	}
	s.drive(o, id, release)
	writeJSON(w, http.StatusOK, map[string]string{"result": ResumeDriving})
}

// drive runs a campaign whose lock this process holds until it ends, halts,
// stops in the error state or the server shuts down.
func (s *Server) drive(o *orders.Orders, id int64, release func()) {
	s.drives.Add(1)
	go func() {
		defer s.drives.Done()
		defer release()
		e := &engine.Engine{Orders: o, Chron: s.Chron, Campaign: id, Out: s.Log}
		res, err := e.Drive(s.ctx)
		e.WaitHooks()
		if s.Log == nil {
			return
		}
		switch {
		case errors.Is(err, engine.ErrInterrupted):
			fmt.Fprintf(s.Log, "[%d] interrupted by shutdown; the current step runs again on resume\n", id)
		case err != nil:
			fmt.Fprintf(s.Log, "[%d] %v\n", id, err)
		default:
			fmt.Fprintf(s.Log, "[%d] stopped: %s\n", id, res)
		}
	}()
}
