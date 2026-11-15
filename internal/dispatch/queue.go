package dispatch

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"

	"hetman/internal/engine"
)

// QueueFile is the name of the queue in the .hetman folder.
const QueueFile = "tasks.db"

// QueuePath is the queue file of a routing file's folder.
func QueuePath(dir string) string { return filepath.Join(dir, ".hetman", QueueFile) }

// Recorded task states. A started task is where its campaign is: see View.
const (
	Queued    = "queued"    // waits for the dispatcher
	Starting  = "starting"  // the dispatcher is creating its campaign
	Started   = "started"   // its campaign exists; the chronicle tells the rest
	Cancelled = "cancelled" // cancelled while queued; never runs
	Done      = "done"      // its campaign reached done; kept once seen, since an ended campaign never changes
	Failed    = "failed"    // its campaign reached failed; kept likewise
)

// Task is one entry of the queue.
type Task struct {
	ID       int64
	Kind     string
	Input    json.RawMessage // a JSON object, without the task field
	Key      string          // de-duplication key; empty for none
	State    string          // one of the recorded states
	Campaign int64           // the campaign that handles it, once started
	Added    time.Time
	Started  time.Time // when the dispatcher took it; zero before
	Ended    time.Time // when its campaign ended, once seen; zero before
}

// Ref is how the task is named in the run input of its campaign.
func (t Task) Ref() engine.TaskRef { return engine.TaskRef{ID: t.ID, Kind: t.Kind, Key: t.Key} }

// RunInput is the run input of the task's campaign: the task's input with
// the task field added.
func (t Task) RunInput() (json.RawMessage, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(t.Input, &in); err != nil {
		return nil, err
	}
	if in == nil {
		in = map[string]json.RawMessage{}
	}
	ref, _ := json.Marshal(t.Ref())
	in[engine.TaskField] = ref
	return json.Marshal(in)
}

// Queue is an open task queue.
type Queue struct {
	db *sql.DB
}

const queueSchema = `
CREATE TABLE IF NOT EXISTS tasks (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	kind       TEXT NOT NULL,
	input      TEXT NOT NULL,
	key        TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL,
	campaign   INTEGER NOT NULL DEFAULT 0,
	added_at   TEXT NOT NULL,
	started_at TEXT NOT NULL DEFAULT '',
	ended_at   TEXT NOT NULL DEFAULT '',
	changed    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tasks_by_key ON tasks(key) WHERE key != '';
CREATE INDEX IF NOT EXISTS tasks_by_state ON tasks(state, id);
`

// OpenQueue opens or creates the queue file at path. Several processes may
// have it open at once; writers wait for each other.
func OpenQueue(path string) (*Queue, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + uriPath(filepath.ToSlash(abs)) +
		"?_pragma=busy_timeout(30000)&_pragma=journal_mode(truncate)&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(queueSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("queue %s: %w", path, err)
	}
	return &Queue{db: db}, nil
}

// uriPath writes an absolute path with forward slashes as the path part of
// a "file:" URI, as the chronicle does: a UNC path keeps an empty authority
// and only the characters that would end or escape the path are encoded.
func uriPath(slashPath string) string {
	var b strings.Builder
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

// Close closes the queue.
func (q *Queue) Close() error { return q.db.Close() }

const timeFormat = time.RFC3339Nano

// ErrNoTask is returned for a task that does not exist.
var ErrNoTask = errors.New("no such task")

// ErrNotQueued is returned when a task that is no longer queued is cancelled.
var ErrNotQueued = errors.New("only a queued task can be cancelled")

// CheckInput checks the input of a new task: one JSON object that does not
// use the field reserved for the task itself. It returns the input compacted.
func CheckInput(data []byte) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, newError(nil, "cli.task.input_not_object", "error", err)
	}
	if obj == nil {
		return nil, newError(nil, "cli.task.input_null")
	}
	if _, ok := obj[engine.TaskField]; ok {
		return nil, newError(nil, "cli.task.input_reserved", "field", strconv.Quote(engine.TaskField))
	}
	var b bytes.Buffer
	if err := json.Compact(&b, data); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Add queues a task. When key is not empty and force is false, a task with
// the same key that was not cancelled, whatever its state, makes Add record
// nothing and return that task with added false. With force the task is
// added all the same.
func (q *Queue) Add(kind string, input json.RawMessage, key string, force bool) (t Task, added bool, err error) {
	tx, err := q.db.Begin()
	if err != nil {
		return t, false, err
	}
	defer tx.Rollback()
	if key != "" && !force {
		row := tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE key = ? AND state != ? ORDER BY id DESC LIMIT 1`, key, Cancelled)
		t, err = scanTask(row)
		if err == nil {
			return t, false, nil
		}
		if !errors.Is(err, ErrNoTask) {
			return t, false, err
		}
	}
	now := time.Now().UTC()
	res, err := tx.Exec(`INSERT INTO tasks(kind, input, key, state, added_at, changed) VALUES(?, ?, ?, ?, ?, `+nextChange+`)`,
		kind, string(input), key, Queued, now.Format(timeFormat))
	if err != nil {
		return t, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return t, false, err
	}
	if err := tx.Commit(); err != nil {
		return t, false, err
	}
	return Task{ID: id, Kind: kind, Input: input, Key: key, State: Queued, Added: now}, true, nil
}

const taskColumns = `id, kind, input, key, state, campaign, added_at, started_at, ended_at`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var t Task
	var input, added, started, ended string
	err := row.Scan(&t.ID, &t.Kind, &input, &t.Key, &t.State, &t.Campaign, &added, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNoTask
	}
	if err != nil {
		return t, err
	}
	t.Input = json.RawMessage(input)
	t.Added, _ = time.Parse(timeFormat, added)
	t.Started, _ = time.Parse(timeFormat, started)
	t.Ended, _ = time.Parse(timeFormat, ended)
	return t, nil
}

func (q *Queue) query(where string, args ...any) ([]Task, error) {
	rows, err := q.db.Query(`SELECT `+taskColumns+` FROM tasks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns one task.
func (q *Queue) Get(id int64) (Task, error) {
	t, err := scanTask(q.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, ErrNoTask) {
		return t, newError(ErrNoTask, "cli.task.no_such_task", "id", id)
	}
	return t, err
}

// List returns up to limit tasks, newest first.
func (q *Queue) List(limit int) ([]Task, error) {
	return q.query(`ORDER BY id DESC LIMIT ?`, limit)
}

// Open returns the tasks the dispatcher still has to look after: queued,
// starting and started ones, oldest first.
func (q *Queue) Open() ([]Task, error) {
	return q.query(`WHERE state IN (?, ?, ?) ORDER BY id`, Queued, Starting, Started)
}

// Cancel cancels a queued task.
func (q *Queue) Cancel(id int64) (Task, error) {
	res, err := q.db.Exec(`UPDATE tasks SET state = ?, changed = `+nextChange+` WHERE id = ? AND state = ?`, Cancelled, id, Queued)
	if err != nil {
		return Task{}, err
	}
	t, err := q.Get(id)
	if err != nil {
		return t, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return t, newError(ErrNotQueued, "cli.task.not_queued", "id", id, "state", t.State)
	}
	return t, nil
}

// claim marks a queued task as starting. It reports false when the task is
// no longer queued, for example because it was cancelled meanwhile.
func (q *Queue) claim(id int64) (bool, error) {
	res, err := q.db.Exec(`UPDATE tasks SET state = ?, started_at = ?, changed = `+nextChange+` WHERE id = ? AND state = ?`,
		Starting, time.Now().UTC().Format(timeFormat), id, Queued)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// link records the campaign of a starting task.
func (q *Queue) link(id, campaign int64) error {
	_, err := q.db.Exec(`UPDATE tasks SET state = ?, campaign = ?, changed = `+nextChange+` WHERE id = ? AND state = ?`, Started, campaign, id, Starting)
	return err
}

// ended records that the campaign of a started task has ended.
func (q *Queue) ended(id int64, end string, at time.Time) error {
	_, err := q.db.Exec(`UPDATE tasks SET state = ?, ended_at = ?, changed = `+nextChange+` WHERE id = ? AND state = ?`, end, at.UTC().Format(timeFormat), id, Started)
	return err
}

// nextChange numbers every write, so that Stamp moves with each one.
const nextChange = `(SELECT COALESCE(MAX(changed), 0) + 1 FROM tasks)`

// Stamp changes whenever a task is added or changes its recorded state, so
// a watcher can tell cheaply whether to look again.
func (q *Queue) Stamp() (int64, error) {
	var n int64
	err := q.db.QueryRow(`SELECT COALESCE(MAX(changed), 0) FROM tasks`).Scan(&n)
	return n, err
}
