package chronicle

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OfKinds returns the events of the given kinds of every campaign in the
// journal, in order. Observations shared by every campaign of a folder,
// such as how much a client was used, are read this way.
func (c *Chronicle) OfKinds(kinds ...string) ([]Event, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	rows, err := c.db.Query(`SELECT seq, campaign, at, kind, node, check_name, attempt, outcome, details
		FROM events WHERE kind IN (?`+strings.Repeat(", ?", len(kinds)-1)+`) ORDER BY seq`, args...)
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

// Tree returns a campaign and every campaign its steps started, directly or
// through other children.
func (c *Chronicle) Tree(top int64) (map[int64]bool, error) {
	rows, err := c.db.Query(`WITH RECURSIVE tree(id) AS (
		SELECT ? UNION SELECT campaigns.id FROM campaigns JOIN tree ON campaigns.parent = tree.id)
		SELECT id FROM tree`, top)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
