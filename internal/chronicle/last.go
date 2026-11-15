package chronicle

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// LastEvent returns the newest event of a campaign, read by a single index
// lookup. It tells cheaply whether something happened to a campaign after
// a known event, such as a retry or rewind after its end.
func (c *Chronicle) LastEvent(campaign int64) (Event, error) {
	var e Event
	var at, details string
	err := c.db.QueryRow(`SELECT seq, campaign, at, kind, node, check_name, attempt, outcome, details
		FROM events WHERE campaign = ? ORDER BY seq DESC LIMIT 1`, campaign).
		Scan(&e.Seq, &e.Campaign, &at, &e.Kind, &e.Node, &e.Check, &e.Attempt, &e.Outcome, &details)
	if errors.Is(err, sql.ErrNoRows) {
		return e, fmt.Errorf("campaign %d: %w", campaign, ErrNoCampaign)
	}
	if err != nil {
		return e, err
	}
	e.At, _ = time.Parse(timeFormat, at)
	if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
		return e, fmt.Errorf("chronicle: event %d: %w", e.Seq, err)
	}
	return e, nil
}
