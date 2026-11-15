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
	ChildStarted    = "child_started" // a segment or for-each step started a child campaign
	ItemFinished    = "item_finished" // the child campaign of a for-each item reached its end
	ItemNotRun      = "item_not_run"  // a for-each item will not run, because an item it needs did not reach done
	LockWaiting     = "lock_waiting"  // a step waits for a lock another step holds
	ItemKept        = "item_kept"     // a retried for-each keeps an item that reached done in the attempt before
	Retried         = "retried"       // a person sent the campaign back to a failed step, with a fresh attempt budget
	Rewound         = "rewound"       // a person sent the campaign back to a node it visited before
	NoClient        = "no_client"     // no client of a step's role can run it now; the campaign waits
	// OutputRepair: the answer of an agent step's attempt does not match its
	// schema, and the engine asks the same conversation to correct it
	// (Round, the rejected answer's problems in Error, Session, the message
	// in Prompt). It is not an attempt; the step_finished of the attempt
	// follows.
	OutputRepair = "output_repair"
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
	FailUnable        = "unable"         // the agent reported it cannot do its work; the campaign stops for a person
	FailDenied        = "denied"         // the client denied tool calls and the node does not accept an answer after that
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
	Instead   string   `json:"instead,omitempty"`   // transition: the outcome the step produced, when a limit turned the flow to Outcome
	Denied    []string `json:"denied,omitempty"`    // agent step: the tool calls the client denied in the attempt
	Error     string   `json:"error,omitempty"`     // why a step or the campaign stopped
	Exit      *int     `json:"exit,omitempty"`      // process exit code
	Millis    int64    `json:"ms,omitempty"`        // step or check duration
	Log       string   `json:"log,omitempty"`       // log file, relative to the campaign folder
	Artifacts []string `json:"artifacts,omitempty"` // from the result file
	Soft      bool     `json:"soft,omitempty"`      // check is soft
	Pid       int      `json:"pid,omitempty"`       // process id; on Unix also its process group id
	Ident     string   `json:"ident,omitempty"`     // process start time, to tell a reused id apart

	Input    json.RawMessage `json:"input,omitempty"`    // run input, on campaign_started
	Language string          `json:"language,omitempty"` // code of the language for the person, on campaign_started; empty for none
	Executor string          `json:"executor,omitempty"` // agent step: executor name, on step_started and step_finished
	Model    string          `json:"model,omitempty"`    // agent step: model string passed through
	Effort   string          `json:"effort,omitempty"`   // agent step: effort string passed through
	Prompt   string          `json:"prompt,omitempty"`   // agent step: rendered prompt file, relative to the campaign folder
	Stderr   string          `json:"stderr,omitempty"`   // agent step: adapter diagnostics file, relative to the campaign folder
	Session  string          `json:"session,omitempty"`  // session id passed to the adapter, or reported by it
	Output   json.RawMessage `json:"output,omitempty"`   // agent step: the validated output
	Failure  string          `json:"failure,omitempty"`  // agent step: why the attempt failed (Fail*)
	Dropped  string          `json:"dropped,omitempty"`  // agent step: a cannot-proceed reason given next to another outcome, left out of the output
	RetryAt  *time.Time      `json:"retry_at,omitempty"` // limit failure: when to try again

	Reason    string         `json:"reason,omitempty"`    // asked, notified: why a person is needed (Reason*)
	Questions []ask.Question `json:"questions,omitempty"` // asked: the questions
	Show      []string       `json:"show,omitempty"`      // asked: files to look at, relative to the orders folder
	Outcomes  []string       `json:"outcomes,omitempty"`  // asked by a human node: the outcomes to choose from
	Round     int            `json:"round,omitempty"`     // asked by a dialogue step: which round of questions; output_repair: which repair of the attempt
	Answers   []ask.Answer   `json:"answers,omitempty"`   // answered: the answers, in question order
	Ask       int64          `json:"ask,omitempty"`       // answered, notified: the seq of the event answered or announced
	By        string         `json:"by,omitempty"`        // answered, retried, rewound: who did it, when known
	Via       string         `json:"via,omitempty"`       // answered, retried, rewound: the command line or the page

	Locks      []string        `json:"locks,omitempty"`       // step_started: the locks the step holds
	Lock       string          `json:"lock,omitempty"`        // lock_waiting: the lock waited for
	Holder     string          `json:"holder,omitempty"`      // lock_waiting: who holds it
	Child      int64           `json:"child,omitempty"`       // child_started, item_finished, step_finished of a segment: the child campaign
	Item       string          `json:"item,omitempty"`        // for-each events and a child's campaign_started: the item id
	Needs      string          `json:"needs,omitempty"`       // item_not_run: the item it needed that did not reach done
	Items      json.RawMessage `json:"items,omitempty"`       // step_started of a for-each: the list, as read when the step started
	Parent     int64           `json:"parent,omitempty"`      // campaign_started of a child: the campaign that started it
	ParentNode string          `json:"parent_node,omitempty"` // campaign_started of a child: the node of the parent that started it

	Role      string     `json:"role,omitempty"`       // step_finished of a step run through a role: the role
	Choice    *Choice    `json:"choice,omitempty"`     // step_started of a step run through a role, no_client: how the client was chosen
	WaitUntil *time.Time `json:"wait_until,omitempty"` // no_client: when the first client can run again; nil for a person
}

// Why a client of a role was chosen, in Choice.Reason.
const (
	ChoseFixed  = "fixed"  // the first client of the list that can run
	ChoseTurn   = "turn"   // round robin: the next client after the one chosen last for the role
	ChoseQuota  = "quota"  // the client that has used the smallest share of its allowance
	ChoseResume = "resume" // the client that ran the step's cut-off attempt or asked its questions continues
	ChoseNone   = "none"   // no_client: no client can run
)

// Choice is how the client of an attempt was chosen. Usage holds what was
// compared, for every client of the role in its order, so that a reader
// needs nothing else to explain the choice.
type Choice struct {
	Role   string        `json:"role"`
	Mode   string        `json:"mode"`           // the role's selection mode
	Client int           `json:"client"`         // the chosen client's position in the role, from 1; 0 for none
	Reason string        `json:"reason"`         // Chose*
	From   int           `json:"from,omitempty"` // the client whose limit made the step change client
	Usage  []ClientUsage `json:"usage"`
}

// ClientUsage is what was known about one client of a role at a choice.
type ClientUsage struct {
	Client   int           `json:"client"` // position in the role, from 1
	Executor string        `json:"executor"`
	Model    string        `json:"model,omitempty"`
	Windows  []WindowUsage `json:"windows,omitempty"`
	// Exhausted: the client cannot run until Until, or, when Until is nil,
	// until a person resumes or its window is seen to reset.
	Exhausted bool       `json:"exhausted,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
	Why       string     `json:"why,omitempty"` // what exhausted it: Source* of the window, or limit
}

// Where the share of a usage window comes from, in WindowUsage.Source.
const (
	SourceMeasured  = "measured"  // reported by the adapter, from the client itself
	SourceEstimated = "estimated" // counted by the engine against the allowance the role states
	SourceLimit     = "limit"     // the client's last attempt hit its limit, which resets at a known time
	SourceUnknown   = "unknown"   // nothing is known: no measurement and no allowance
)

// WindowUsage is the usage of one window of a client.
type WindowUsage struct {
	Name      string     `json:"name"`           // five_hour or weekly
	Source    string     `json:"source"`         // Source*
	Used      *float64   `json:"used,omitempty"` // share of the allowance used; nil when unknown
	ResetsAt  *time.Time `json:"resets_at,omitempty"`
	At        *time.Time `json:"at,omitempty"`        // measured: when the adapter was asked
	Attempts  int        `json:"attempts,omitempty"`  // attempts the engine saw in the window
	Millis    int64      `json:"ms,omitempty"`        // their total duration
	Allowance string     `json:"allowance,omitempty"` // estimated: the allowance, as written in the orders
}

// Campaign is one execution of a flow.
type Campaign struct {
	ID      int64
	Flow    string
	Orders  string
	Started time.Time
	Parent  int64 // the campaign whose segment or for-each step started this one; 0 for a campaign started by a person
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
CREATE INDEX IF NOT EXISTS events_by_kind ON events(kind, seq);
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
	if err := addParent(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("chronicle %s: %w", path, err)
	}
	return &Chronicle{db: db}, nil
}

// addParent adds the parent column to a journal written before child
// campaigns existed. Two processes may try at once; one of them wins.
func addParent(db *sql.DB) error {
	if _, err := db.Exec(`SELECT parent FROM campaigns LIMIT 0`); err == nil {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE campaigns ADD COLUMN parent INTEGER NOT NULL DEFAULT 0`)
	if err != nil {
		if _, again := db.Exec(`SELECT parent FROM campaigns LIMIT 0`); again == nil {
			return nil
		}
	}
	return err
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
// together with its run input (a JSON object; nil for none) and the code of
// the language for the person (empty for none). Both stay as recorded for
// the whole campaign.
func (c *Chronicle) NewCampaign(flow, ordersPath, start string, input json.RawMessage, language string) (int64, error) {
	return c.newCampaign(flow, ordersPath, start, Details{Input: input, Language: language})
}

// NewChild creates the child campaign a step of campaign parent starts at
// node to run a segment, item being the for-each item it runs ("" for a
// segment node).
func (c *Chronicle) NewChild(parent int64, node, item, flow, ordersPath, start string, input json.RawMessage, language string) (int64, error) {
	return c.newCampaign(flow, ordersPath, start, Details{Input: input, Language: language, Parent: parent, ParentNode: node, Item: item})
}

func (c *Chronicle) newCampaign(flow, ordersPath, start string, d Details) (int64, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(timeFormat)
	res, err := tx.Exec(`INSERT INTO campaigns(flow, orders, started_at, parent) VALUES(?, ?, ?, ?)`, flow, ordersPath, now, d.Parent)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	details, err := json.Marshal(d)
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
	err := c.db.QueryRow(`SELECT id, flow, orders, started_at, parent FROM campaigns WHERE id = ?`, id).
		Scan(&k.ID, &k.Flow, &k.Orders, &started, &k.Parent)
	if errors.Is(err, sql.ErrNoRows) {
		return k, fmt.Errorf("campaign %d: %w", id, ErrNoCampaign)
	}
	k.Started, _ = time.Parse(timeFormat, started)
	return k, err
}

// Latest returns the most recently created campaign a person started.
func (c *Chronicle) Latest() (Campaign, error) {
	var id int64
	err := c.db.QueryRow(`SELECT id FROM campaigns WHERE parent = 0 ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Campaign{}, ErrNoCampaign
	}
	if err != nil {
		return Campaign{}, err
	}
	return c.Campaign(id)
}

// Campaigns returns up to limit campaigns a person started, newest first;
// child campaigns are reached through their parents.
func (c *Chronicle) Campaigns(limit int) ([]Campaign, error) {
	rows, err := c.db.Query(`SELECT id, flow, orders, started_at, parent FROM campaigns WHERE parent = 0 ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		var k Campaign
		var started string
		if err := rows.Scan(&k.ID, &k.Flow, &k.Orders, &started, &k.Parent); err != nil {
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
	LockWait      string         // the lock the next step at Node waits for; empty when it waits for none
	LockHolder    string         // who holds LockWait
	Children      []int64        // child campaigns of the running step at Node that have not reached their end
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
			s.LockWait, s.LockHolder, s.Children = "", "", nil
		case LockWaiting:
			s.LockWait, s.LockHolder = e.Lock, e.Holder
		case ChildStarted:
			if s.Running && e.Node == s.Node && e.Attempt == s.Attempt {
				s.Children = append(s.Children, e.Child)
			}
		case ItemFinished:
			for i, id := range s.Children {
				if id == e.Child {
					s.Children = append(s.Children[:i:i], s.Children[i+1:]...)
					break
				}
			}
		case StepInterrupted:
			s.Running = false
		case StepFinished:
			s.Running, s.Children = false, nil
			s.HasOutcome, s.Outcome = e.Outcome != "", e.Outcome
			s.Ask, s.Answer = nil, nil
			if Counts(e.Failure) {
				s.Failures++
			}
			switch {
			case e.Failure == FailLimit && e.Role != "":
				// A step run through a role does not wait here: the engine
				// chooses another client next, or records no_client.
			case e.Failure == FailLimit:
				s.Waiting = WaitLimit
				if e.RetryAt != nil {
					s.WaitUntil = *e.RetryAt
				}
			case e.Failure == FailOffline:
				s.Waiting = WaitOffline
			}
		case NoClient:
			s.Waiting, s.WaitUntil = WaitLimit, time.Time{}
			if e.WaitUntil != nil {
				s.WaitUntil = *e.WaitUntil
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
		case Retried, Rewound:
			// The node becomes the position again as if the flow had just
			// moved there: the end, an error stop, a halt, a wait and an
			// open question are left behind, and the attempt budget of the
			// visit starts afresh. Attempts keep counting, so no file of an
			// earlier attempt is written over.
			s.Node, s.End, s.Error = e.Node, "", ""
			s.Running, s.Attempt, s.HasOutcome, s.Outcome, s.Failures = false, 0, false, "", 0
			s.HaltRequested, s.Halted = false, false
			s.Waiting, s.WaitUntil = "", time.Time{}
			s.Ask, s.Answer = nil, nil
			s.LockWait, s.LockHolder, s.Children = "", "", nil
		case Stopped:
			s.Error = e.Error
		case HaltRequested:
			s.HaltRequested = true
		case Halted:
			s.Halted = true
			s.LockWait, s.LockHolder = "", ""
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
