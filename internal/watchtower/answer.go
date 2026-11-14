package watchtower

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/engine"
)

// handleShow sends a file a step asks a person to look at: item i of the
// show list of the asked event with seq ask. Only files named that way in
// the chronicle can be read, from the orders folder.
func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	seq, _ := strconv.ParseInt(r.URL.Query().Get("ask"), 10, 64)
	i, _ := strconv.Atoi(r.URL.Query().Get("i"))
	events, err := s.Chron.Events(id)
	if err != nil {
		writeError(w, err)
		return
	}
	rel := ""
	for _, e := range events {
		if e.Kind == chronicle.Asked && e.Seq == seq && i >= 0 && i < len(e.Show) {
			rel = e.Show[i]
		}
	}
	if rel == "" || !filepath.IsLocal(filepath.FromSlash(rel)) {
		http.Error(w, "no such file to show", http.StatusNotFound)
		return
	}
	o, _ := s.currentOrders()
	f, err := os.Open(filepath.Join(o.Dir, filepath.FromSlash(rel)))
	if err != nil {
		http.Error(w, "the file does not exist now", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not a file", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// AnswerRequest is the body of an answer request.
type AnswerRequest struct {
	Ask      int64             `json:"ask"`      // the seq of the asked event answered
	Values   map[string]string `json:"values"`   // question id to answer
	Comments map[string]string `json:"comments"` // question id to comment
	Outcome  string            `json:"outcome"`  // the outcome chosen at a human node
	By       string            `json:"by"`       // who answers, when given
}

// AnswerRecorded is the result of an answer to a campaign that is halted or
// in the error state: it continues on resume.
const AnswerRecorded = "recorded"

// handleAnswer records a person's answers through the same check as the
// answer command. Then the campaign continues: in the process that drives
// it, or else driven by this server, as after a resume from the page.
func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req AnswerRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, fmt.Errorf("%w: %v", errBadAnswer, err))
		return
	}
	reply := engine.Reply{Ask: req.Ask, By: strings.TrimSpace(req.By), Via: engine.ViaPage,
		Given: ask.Given{Values: req.Values, Comments: req.Comments, Outcome: req.Outcome}}
	if _, err := engine.Answer(s.Chron, id, reply); err != nil {
		writeError(w, err)
		return
	}
	_, st, err := engine.Pending(s.Chron, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.HaltRequested || st.Error != "" {
		writeJSON(w, http.StatusOK, map[string]string{"result": AnswerRecorded})
		return
	}
	o, _ := s.currentOrders()
	release, err := engine.Lock(o, id)
	switch {
	case errors.Is(err, engine.ErrLocked):
		writeJSON(w, http.StatusOK, map[string]string{"result": ResumeElsewhere})
	case err != nil:
		writeError(w, err)
	default:
		s.drive(o, id, release)
		writeJSON(w, http.StatusOK, map[string]string{"result": ResumeDriving})
	}
}
