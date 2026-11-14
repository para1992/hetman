// Package chronicle is the event journal of campaigns, stored in SQLite.
//
// Events are only appended. Where a campaign stands is always derived by
// replaying its events (see Replay); nothing else holds campaign state.
package chronicle

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"

	"hetman/internal/ask"
)

// Event kinds.
const (
	CampaignStarted = "campaign_started"
	StepStarted     = "step_started"
	StepFinished    = "step_finished"
	StepInterrupted = "step_interrupted"
	ProcessStarted  = "process_started" // the process of a step or check is running
	ProcessCleanup  = "process_cleanup" // what was done with a process left by an interrupted step
	CheckFinished   = "check_finished"
	Transition      = "transition"
	Stopped         = "error"
	HaltRequested   = "halt_requested"
	Halted          = "halted"
	Resumed         = "resumed"
	AgentSession    = "agent_session" // an adapter reported the session id of its conversation
	Asked           = "asked"         // a step asks a person questions; the campaign waits for the answers
	Answered        = "answered"      // a person answered the questions of an asked event
	Notified        = "notified"      // the notification hook ran for an event that made the campaign wait for a person
)

// Why a campaign waits for a person, recorded as Details.Reason of an asked
// event and of a notified event.
const (
	ReasonGate     = "gate"     // a gate asks its questions
	ReasonHuman    = "human"    // a human node asks for a decision
	ReasonDialogue = "dialogue" // an agent step asks questions in the middle of its work
	ReasonOffline  = "offline"  // an agent step lost connectivity
	ReasonLimit    = "limit"    // an agent step hit a subscription limit with no time to retry
	ReasonError    = "error"    // the campaign stopped in the error state
)

// Why an attempt of an agent step failed, recorded as Details.Failure of
// its step_finished event.
const (
	FailInvalidOutput = "invalid_output" // the output does not match the node's schema
	FailTransient     = "transient"      // the adapter reported a transient error
	FailPermanent     = "permanent"      // the adapter reported a permanent error; the node fails at once
	FailCrash         = "crash"          // the adapter exited without a result, or with a non-zero code
	FailProtocol      = "protocol"       // the adapter wrote something that breaks the protocol
	FailTimeout       = "timeout"        // the attempt ran longer than the node's timeout
	FailIdle          = "idle_timeout"   // the adapter sent no message for longer than the node's idle_timeout
	FailLimit         = "limit"          // the subscription limit is reached; the attempt does not count
	FailOffline       = "offline"        // no connectivity; the attempt does not count
)

// Counts reports whether a failed attempt uses up the attempt budget of
// its node visit.
func Counts(failure string) bool {
	return failure != "" && failure != FailLimit && failure != FailOffline
}

// Event is one journal entry. Fields that do not apply to a kind stay empty.
type Event struct {
	Seq      int64
	Campaign int64
	At       time.Time
	Kind     string
	Node     string
	Check    string
	Attempt  int
	Outcome  string
	Details
}

// Details are stored as JSON next to the indexed columns.
type Details struct {
	To        string   `json:"to,omitempty"`        // transition target
	Error     string   `json:"error,omitempty"`     // why a step or the campaign stopped
	Exit      *int     `json:"exit,omitempty"`      // process exit code
	Millis    int64    `json:"ms,omitempty"`        // step or check duration
	Log       string   `json:"log,omitempty"`       // log file, relative to the campaign folder
	Artifacts []string `json:"artifacts,omitempty"` // from the result file
	Soft      bool     `json:"soft,omitempty"`      // check is soft
	Pid       int      `json:"pid,omitempty"`       // process id; on Unix also its process group id
	Ident     string   `json:"ident,omitempty"`     // process start time, to tell a reused id apart

	Input    json.RawMessage `json:"input,omitempty"`    // run input, on campaign_started
	Executor string          `json:"executor,omitempty"` // agent step: executor name
	Model    string          `json:"model,omitempty"`    // agent step: model string passed through
	Effort   string          `json:"effort,omitempty"`   // agent step: effort string passed through
	Prompt   string          `json:"prompt,omitempty"`   // agent step: rendered prompt file, relative to the campaign folder
	Stderr   string          `json:"stderr,omitempty"`   // agent step: adapter diagnostics file, relative to the campaign folder
	Session  string          `json:"session,omitempty"`  // session id passed to the adapter, or reported by it
	Output   json.RawMessage `json:"output,omitempty"`   // agent step: the validated output
	Failure  string          `json:"failure,omitempty"`  // agent step: why the attempt failed (Fail*)
	RetryAt  *time.Time      `json:"retry_at,omitempty"` // limit failure: when to try again

	Reason    string         `json:"reason,omitempty"`    // asked, notified: why a person is needed (Reason*)
	Questions []ask.Question `json:"questions,omitempty"` // asked: the questions
	Show      []string       `json:"show,omitempty"`      // asked: files to look at, relative to the orders folder
	Outcomes  []string       `json:"outcomes,omitempty"`  // asked by a human node: the outcomes to choose from
	Round     int            `json:"round,omitempty"`     // asked by a dialogue step: which round of questions
	Answers   []ask.Answer   `json:"answers,omitempty"`   // answered: the answers, in question order
	Ask       int64          `json:"ask,omitempty"`       // answered, notified: the seq of the event answered or announced
	By        string         `json:"by,omitempty"`        // answered: who answered, when known
	Via       string         `json:"via,omitempty"`       // answered: the command line or the page
}

// Campaign is one execution of a flow.
type Campaign struct {
	ID      int64
	Flow    string
	Orders  string
	Started time.Time
}

// ErrNoCampaign is returned when a campaign does not exist.
var ErrNoCampaign = errors.New("no such campaign")

// Chronicle is an open journal.
type Chronicle struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS campaigns (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	flow       TEXT NOT NULL,
	orders     TEXT NOT NULL,
	started_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	campaign   INTEGER NOT NULL REFERENCES campaigns(id),
	at         TEXT NOT NULL,
	kind       TEXT NOT NULL,
	node       TEXT NOT NULL DEFAULT '',
	check_name TEXT NOT NULL DEFAULT '',
	attempt    INTEGER NOT NULL DEFAULT 0,
	outcome    TEXT NOT NULL DEFAULT '',
	details    TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS events_by_campaign ON events(campaign, seq);
`

// Open opens or creates the journal file at path. Several processes may
// have it open at once; writers wait for each other.
func Open(path string) (*Chronicle, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The rollback journal is truncated instead of deleted after each
	// transaction: on Windows a file another process has open cannot be
	// deleted, which would fail a commit while a second hetman process reads.
	dsn := fileURI(filepath.ToSlash(abs)) +
		"?_pragma=busy_timeout(30000)&_pragma=journal_mode(truncate)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("chronicle %s: %w", path, err)
	}
	return &Chronicle{db: db}, nil
}

// fileURI turns an absolute path with forward slashes into the "file:" URI
// the driver opens. SQLite decodes the path part of the URI and hands it to
// the file system as is, so nothing may be added to it: "file:///C:/x" would
// reach the file system as "/C:/x". A path is therefore written without an
// authority ("file:C:/x", "file:/home/x"), except a UNC path, which gets an
// empty authority so that its leading "//" stays part of the path. Only the
// characters that would end or escape the path are percent-encoded.
func fileURI(slashPath string) string {
	var b strings.Builder
	b.WriteString("file:")
	if strings.HasPrefix(slashPath, "//") {
		b.WriteString("//")
	}
	for i := 0; i < len(slashPath); i++ {
		switch c := slashPath[i]; c {
		case '%', '?', '#', ' ':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Close closes the journal.
func (c *Chronicle) Close() error { return c.db.Close() }

const timeFormat = time.RFC3339Nano

// NewCampaign creates a campaign and records its start at node start,
// together with its run input (a JSON object; nil for none).
func (c *Chronicle) NewCampaign(flow, ordersPath, start string, input json.RawMessage) (int64, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(timeFormat)
	res, err := tx.Exec(`INSERT INTO campaigns(flow, orders, started_at) VALUES(?, ?, ?)`, flow, ordersPath, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	details, err := json.Marshal(Details{Input: input})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO events(campaign, at, kind, node, details) VALUES(?, ?, ?, ?, ?)`, id, now, CampaignStarted, start, string(details)); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// Campaign returns a campaign by id.
func (c *Chronicle) Campaign(id int64) (Campaign, error) {
	var k Campaign
	var started string
	err := c.db.QueryRow(`SELECT id, flow, orders, started_at FROM campaigns WHERE id = ?`, id).
		Scan(&k.ID, &k.Flow, &k.Orders, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return k, fmt.Errorf("campaign %d: %w", id, ErrNoCampaign)
	}
	k.Started, _ = time.Parse(timeFormat, started)
	return k, err
}

// Latest returns the most recently created campaign.
func (c *Chronicle) Latest() (Campaign, error) {
	var id int64
	err := c.db.QueryRow(`SELECT id FROM campaigns ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Campaign{}, ErrNoCampaign
	}
	if err != nil {
		return Campaign{}, err
	}
	return c.Campaign(id)
}

// Campaigns returns up to limit campaigns, newest first.
func (c *Chronicle) Campaigns(limit int) ([]Campaign, error) {
	rows, err := c.db.Query(`SELECT id, flow, orders, started_at FROM campaigns ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		var k Campaign
		var started string
		if err := rows.Scan(&k.ID, &k.Flow, &k.Orders, &started); err != nil {
			return nil, err
		}
		k.Started, _ = time.Parse(timeFormat, started)
		out = append(out, k)
	}
	return out, rows.Err()
}

// LastSeq returns the sequence number of the newest event of any campaign,
// or 0 for an empty journal. Every change to the journal raises it, and it
// is a single index lookup, so it is cheap to ask often.
func (c *Chronicle) LastSeq() (int64, error) {
	var seq int64
	err := c.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}

// Append records an event. Seq and At are assigned here.
func (c *Chronicle) Append(e Event) (Event, error) {
	e.At = time.Now().UTC()
	details, err := json.Marshal(e.Details)
	if err != nil {
		return e, err
	}
	res, err := c.db.Exec(`INSERT INTO events(campaign, at, kind, node, check_name, attempt, outcome, details)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Campaign, e.At.Format(timeFormat), e.Kind, e.Node, e.Check, e.Attempt, e.Outcome, string(details))
	if err != nil {
		return e, fmt.Errorf("chronicle: record %s: %w", e.Kind, err)
	}
	e.Seq, err = res.LastInsertId()
	return e, err
}

// Events returns every event of a campaign in order.
func (c *Chronicle) Events(campaign int64) ([]Event, error) {
	rows, err := c.db.Query(`SELECT seq, campaign, at, kind, node, check_name, attempt, outcome, details
		FROM events WHERE campaign = ? ORDER BY seq`, campaign)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at, details string
		if err := rows.Scan(&e.Seq, &e.Campaign, &at, &e.Kind, &e.Node, &e.Check, &e.Attempt, &e.Outcome, &details); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(timeFormat, at)
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
			return nil, fmt.Errorf("chronicle: event %d: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// State is where a campaign stands, derived from its events.
type State struct {
	Node          string         // current position
	Running       bool           // a step at Node started and has not finished
	Attempt       int            // attempt of the running or last finished step at Node
	HasOutcome    bool           // the step at Node finished and its transition is not recorded yet
	Outcome       string         // that outcome
	End           string         // done or failed once the campaign has ended
	Error         string         // set while the campaign is stopped in the error state
	HaltRequested bool           // a halt is in force until resumed
	Halted        bool           // the halt has taken effect
	Attempts      map[string]int // started executions per node
	Failures      int            // failed attempts at Node that count against its budget, this visit
	Waiting       string         // limit or offline: the step at Node waits before it runs again
	WaitUntil     time.Time      // for a limit: when the step runs again on its own; zero means a person resumes
	Ask           *Event         // the asked event of the step at Node until that step has used its answers
	Answer        *Event         // the answered event of Ask, once a person has answered
}

// Asking reports whether the campaign waits for a person to answer Ask.
func (s State) Asking() bool { return s.Ask != nil && s.Answer == nil }

// Reasons a campaign waits, in State.Waiting.
const (
	WaitLimit   = "limit"
	WaitOffline = "offline"
)

// Replay derives the state of a campaign from its events.
func Replay(events []Event) State {
	s := State{Attempts: map[string]int{}}
	for _, e := range events {
		switch e.Kind {
		case CampaignStarted:
			s.Node = e.Node
		case StepStarted:
			s.Node, s.Running, s.Attempt, s.HasOutcome = e.Node, true, e.Attempt, false
			s.Attempts[e.Node]++
			s.Waiting, s.WaitUntil = "", time.Time{}
			s.Ask, s.Answer = nil, nil
		case StepInterrupted:
			s.Running = false
		case StepFinished:
			s.Running = false
			s.HasOutcome, s.Outcome = e.Outcome != "", e.Outcome
			s.Ask, s.Answer = nil, nil
			if Counts(e.Failure) {
				s.Failures++
			}
			switch e.Failure {
			case FailLimit:
				s.Waiting = WaitLimit
				if e.RetryAt != nil {
					s.WaitUntil = *e.RetryAt
				}
			case FailOffline:
				s.Waiting = WaitOffline
			}
		case Asked:
			ev := e
			s.Ask, s.Answer, s.Running = &ev, nil, false
		case Answered:
			// Only the first answer to the question still open counts.
			if s.Ask != nil && s.Answer == nil && e.Ask == s.Ask.Seq {
				ev := e
				s.Answer = &ev
			}
		case Transition:
			s.HasOutcome, s.Failures = false, 0
			s.Ask, s.Answer = nil, nil
			if e.To == "done" || e.To == "failed" {
				s.End = e.To
			} else {
				s.Node, s.Attempt = e.To, 0
			}
		case Stopped:
			s.Error = e.Error
		case HaltRequested:
			s.HaltRequested = true
		case Halted:
			s.Halted = true
		case Resumed:
			s.Error, s.HaltRequested, s.Halted = "", false, false
			// A person resuming answers a wait for a person; a wait for a
			// known time continues until then.
			if s.WaitUntil.IsZero() {
				s.Waiting = ""
			}
		}
	}
	return s
}
