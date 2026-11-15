package watchtower

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"hetman/internal/dispatch"
)

// hub notices changes to the chronicle and wakes the connected streams.
//
// A campaign is usually driven by another process, so there is no in-process
// signal for its events. While at least one stream is connected, a single
// watcher looks at the newest sequence number of the chronicle every poll
// interval and wakes every stream when it moved. Once a second the streams
// are woken anyway, because whether a process still drives a campaign (its
// lock) and the orders file can change without a new event. Each stream then
// sends only what differs from what it sent before. With no stream connected
// the watcher stops.
type hub struct {
	mu      sync.Mutex
	subs    map[chan struct{}]struct{}
	running bool
	looks   atomic.Int64 // chronicle looks made so far, for tests
}

// recheckEvery is the number of poll intervals between unconditional wakes.
func (s *Server) recheckEvery() int {
	n := int(time.Second / s.poll())
	if n < 1 {
		return 1
	}
	return n
}

func (s *Server) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.hub.subs == nil {
		s.hub.subs = map[chan struct{}]struct{}{}
	}
	s.hub.subs[ch] = struct{}{}
	if !s.hub.running {
		s.hub.running = true
		go s.watch()
	}
	return ch
}

func (s *Server) unsubscribe(ch chan struct{}) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	delete(s.hub.subs, ch)
}

func (s *Server) watch() {
	t := time.NewTicker(s.poll())
	defer t.Stop()
	last, lastTasks := int64(-1), int64(-1)
	for tick := 1; ; tick++ {
		select {
		case <-t.C:
		case <-s.ctx.Done():
			s.hub.mu.Lock()
			s.hub.running = false
			s.hub.mu.Unlock()
			return
		}
		s.hub.mu.Lock()
		if len(s.hub.subs) == 0 {
			s.hub.running = false
			s.hub.mu.Unlock()
			return
		}
		s.hub.mu.Unlock()
		seq, err := s.Chron.LastSeq()
		stamp := s.taskStamp()
		s.hub.looks.Add(1)
		if err == nil && seq == last && stamp == lastTasks && tick%s.recheckEvery() != 0 {
			continue
		}
		last, lastTasks = seq, stamp
		s.hub.mu.Lock()
		for ch := range s.hub.subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		s.hub.mu.Unlock()
	}
}

// sse writes server-sent events.
type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func startSSE(w http.ResponseWriter) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	// The browser reconnects by itself after this many milliseconds.
	fmt.Fprint(w, "retry: 2000\n\n")
	s.rc.Flush()
	return s
}

func (s *sse) send(event string, data []byte) error {
	var b bytes.Buffer
	b.WriteString("event: " + event + "\n")
	for _, line := range bytes.Split(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := s.w.Write(b.Bytes()); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sse) ping() error {
	if _, err := io.WriteString(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}

const pingEvery = 15 * time.Second

// handleStream keeps a page current. Without a campaign parameter it sends
// "list" events with the campaign list, "roles" events with the roles of
// the served orders once they declare any, and, once the folder has a task
// queue, "tasks" events with the tasks; with one it sends "campaign" events
// with the campaign view and "chronicle" events with the events not sent
// yet. The first event of each kind after connecting carries everything
// ("reset": true for the chronicle), so a reconnected page starts over.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	var id int64
	if v := r.URL.Query().Get("campaign"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeError(w, errBadID)
			return
		}
		if _, err := s.Chron.Campaign(n); err != nil {
			writeError(w, err)
			return
		}
		id = n
	}
	p := s.printer(r)
	wake := s.subscribe()
	defer s.unsubscribe(wake)
	out := startSSE(w)
	var lastView, lastTasks, lastRoles []byte
	sentSeq := int64(-1)
	update := func() error {
		if id == 0 {
			list, err := s.summaries()
			if err != nil {
				return err
			}
			data, _ := json.Marshal(list)
			if !bytes.Equal(data, lastView) {
				lastView = data
				if err := out.send("list", data); err != nil {
					return err
				}
			}
			if roles := s.roleViews(); roles != nil || lastRoles != nil {
				data, _ = json.Marshal(nonNilRoles(roles))
				if !bytes.Equal(data, lastRoles) {
					lastRoles = data
					if err := out.send("roles", data); err != nil {
						return err
					}
				}
			}
			tasks, err := s.taskViews(p)
			if err != nil {
				return err
			}
			if tasks == nil && lastTasks == nil {
				return nil
			}
			data, _ = json.Marshal(nonNilTasks(tasks))
			if !bytes.Equal(data, lastTasks) {
				lastTasks = data
				return out.send("tasks", data)
			}
			return nil
		}
		c, events, err := s.campaign(p, id)
		if err != nil {
			return err
		}
		data, _ := json.Marshal(c)
		if !bytes.Equal(data, lastView) {
			lastView = data
			if err := out.send("campaign", data); err != nil {
				return err
			}
		}
		var fresh []Event
		for _, e := range eventsJSON(events) {
			if e.Seq > sentSeq {
				fresh = append(fresh, e)
			}
		}
		if len(fresh) > 0 || sentSeq < 0 {
			data, _ := json.Marshal(map[string]any{"reset": sentSeq < 0, "events": nonNil(fresh)})
			if err := out.send("chronicle", data); err != nil {
				return err
			}
			if len(fresh) > 0 {
				sentSeq = fresh[len(fresh)-1].Seq
			} else {
				sentSeq = 0
			}
		}
		return nil
	}
	if err := update(); err != nil {
		return
	}
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-ping.C:
			if out.ping() != nil {
				return
			}
		case <-wake:
			if update() != nil {
				return
			}
		}
	}
}

func nonNilTasks(v []dispatch.View) []dispatch.View {
	if v == nil {
		return []dispatch.View{}
	}
	return v
}

func nonNil(e []Event) []Event {
	if e == nil {
		return []Event{}
	}
	return e
}

const (
	logChunk = 64 << 10  // largest piece of log sent in one event
	logTail  = 256 << 10 // how much of a long log is sent on connecting
)

// handleLog follows a log file of a campaign. It sends "chunk" events with
// {"offset", "text"} as the file grows, starting at the "from" byte offset
// (only the last part of a long file when "from" is absent), a "skipped"
// event with the number of bytes left out at the start, and a "reset" event
// when the file was replaced by a shorter one. The file is looked at every
// poll interval while the page follows it.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	id, err := campaignID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.Chron.Campaign(id); err != nil {
		writeError(w, err)
		return
	}
	rel := r.URL.Query().Get("path")
	if !strings.HasPrefix(rel, "logs/") {
		writeError(w, errBadPath)
		return
	}
	path, err := s.campaignFile(id, rel)
	if err != nil {
		writeError(w, err)
		return
	}
	offset := int64(-1)
	if v := r.URL.Query().Get("from"); v != "" {
		if offset, err = strconv.ParseInt(v, 10, 64); err != nil || offset < 0 {
			writeError(w, errBadPath)
			return
		}
	}
	out := startSSE(w)
	t := time.NewTicker(s.poll())
	defer t.Stop()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	buf := make([]byte, logChunk)
	for {
		if err := s.followLog(out, path, &offset, buf); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-ping.C:
			if out.ping() != nil {
				return
			}
		case <-t.C:
		}
	}
}

// followLog sends what was added to the file since offset.
func (s *Server) followLog(out *sse, path string, offset *int64, buf []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // not created yet
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	size := fi.Size()
	if *offset < 0 {
		*offset = 0
		if size > logTail {
			*offset = size - logTail
			data, _ := json.Marshal(map[string]int64{"bytes": *offset})
			if err := out.send("skipped", data); err != nil {
				return err
			}
		}
	}
	if size < *offset {
		*offset = 0
		if err := out.send("reset", []byte("{}")); err != nil {
			return err
		}
	}
	for *offset < size {
		n, err := f.ReadAt(buf, *offset)
		if n == 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				return nil
			}
			break
		}
		piece := buf[:completeRunes(buf[:n])]
		if len(piece) == 0 {
			// A single incomplete character at the end: wait for the rest.
			break
		}
		data, _ := json.Marshal(map[string]any{"offset": *offset, "text": string(piece)})
		if err := out.send("chunk", data); err != nil {
			return err
		}
		*offset += int64(len(piece))
	}
	return nil
}

// completeRunes returns the length of b without a UTF-8 character cut off
// at its end, so that a character split between two reads is sent whole.
func completeRunes(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}
