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
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Event kinds.
const (
	CampaignStarted = "campaign_started"
	StepStarted     = "step_started"
	StepFinished    = "step_finished"
	StepInterrupted = "step_interrupted"
	CheckFinished   = "check_finished"
	Transition      = "transition"
	Stopped         = "error"
	HaltRequested   = "halt_requested"
	Halted          = "halted"
	Resumed         = "resumed"
)

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
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive path: file:///C:/...
	}
	dsn := (&url.URL{Scheme: "file", Path: p}).String() +
		"?_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_txlock=immediate"
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

// Close closes the journal.
func (c *Chronicle) Close() error { return c.db.Close() }

const timeFormat = time.RFC3339Nano

// NewCampaign creates a campaign and records its start at node start.
func (c *Chronicle) NewCampaign(flow, ordersPath, start string) (int64, error) {
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
	if _, err := tx.Exec(`INSERT INTO events(campaign, at, kind, node) VALUES(?, ?, ?, ?)`, id, now, CampaignStarted, start); err != nil {
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
}

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
		case StepInterrupted:
			s.Running = false
		case StepFinished:
			s.Running = false
			s.HasOutcome, s.Outcome = e.Outcome != "", e.Outcome
		case Transition:
			s.HasOutcome = false
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
		}
	}
	return s
}
