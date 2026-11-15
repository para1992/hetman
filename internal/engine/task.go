package engine

import (
	"encoding/json"
)

// TaskField is the run input field that names the task a campaign handles
// when a dispatcher started it. Flows and scripts read it like any other
// input field, for example {{input.hetman_task.kind}} in a prompt.
const TaskField = "hetman_task"

// TaskRef identifies the task of a campaign.
type TaskRef struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
}

// TaskOf returns the task named in a run input, or nil when the input names
// none.
func TaskOf(input json.RawMessage) *TaskRef {
	var in map[string]json.RawMessage
	if json.Unmarshal(input, &in) != nil || in[TaskField] == nil {
		return nil
	}
	var t TaskRef
	if json.Unmarshal(in[TaskField], &t) != nil || t.ID <= 0 {
		return nil
	}
	return &t
}

// task returns the task of the campaign, or nil.
func (e *Engine) task() *TaskRef {
	if _, err := e.syncInput(); err != nil {
		return nil
	}
	e.inputMu.Lock()
	defer e.inputMu.Unlock()
	return TaskOf(e.input)
}

// LockPath makes the calling process the only holder of the lock file at
// path until the returned release is called or the process exits. It
// returns ErrLocked at once when another process holds it.
func LockPath(path string) (func(), error) { return lockFile(path) }
