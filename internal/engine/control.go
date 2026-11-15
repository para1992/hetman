package engine

import (
	"errors"
	"fmt"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// ErrEnded is returned when a halt or resume is asked of a campaign that has
// already reached done or failed.
var ErrEnded = errors.New("campaign has already ended")

// How a halt request was taken.
const (
	HaltAlready = "already" // a halt was already in force
	HaltPending = "pending" // another process drives; it halts after its current step
	HaltNow     = "halted"  // nobody drives; the halt took effect at once
)

// Halt asks a campaign to halt after its current step. The request is a
// chronicle event, so it reaches whichever process drives the campaign. If
// no process does, the halt takes effect immediately. The returned state is
// the one before the request.
func Halt(o *orders.Orders, ch *chronicle.Chronicle, id int64) (string, chronicle.State, error) {
	if err := notChild(ch, id); err != nil {
		return "", chronicle.State{}, err
	}
	st, err := state(ch, id)
	if err != nil {
		return "", st, err
	}
	switch {
	case st.End != "":
		return "", st, fmt.Errorf("campaign %d: %w (%s)", id, ErrEnded, st.End)
	case st.HaltRequested:
		return HaltAlready, st, nil
	}
	if _, err := ch.Append(chronicle.Event{Campaign: id, Kind: chronicle.HaltRequested, Node: st.Node}); err != nil {
		return "", st, err
	}
	release, err := Lock(o, id)
	if errors.Is(err, ErrLocked) {
		return HaltPending, st, nil
	}
	if err != nil {
		return "", st, err
	}
	defer release()
	if _, err := ch.Append(chronicle.Event{Campaign: id, Kind: chronicle.Halted, Node: st.Node}); err != nil {
		return "", st, err
	}
	return HaltNow, st, nil
}

// Resume lifts a halt or an error stop of a campaign and takes its lock.
// When another process drives the campaign, release is nil and the campaign
// continues there. Otherwise the caller holds the lock and must drive the
// campaign, then call release. The returned state is the one before.
func Resume(o *orders.Orders, ch *chronicle.Chronicle, id int64) (release func(), st chronicle.State, err error) {
	if err := notChild(ch, id); err != nil {
		return nil, st, err
	}
	st, err = state(ch, id)
	if err != nil {
		return nil, st, err
	}
	if st.End != "" {
		return nil, st, fmt.Errorf("campaign %d: %w (%s)", id, ErrEnded, st.End)
	}
	if _, err := ch.Append(chronicle.Event{Campaign: id, Kind: chronicle.Resumed, Node: st.Node}); err != nil {
		return nil, st, err
	}
	release, err = Lock(o, id)
	if errors.Is(err, ErrLocked) {
		return nil, st, nil
	}
	return release, st, err
}

// ErrChild is returned when a halt or resume is asked of a child campaign:
// it is halted and resumed with the campaign a person started.
var ErrChild = errors.New("is run by another campaign")

func notChild(ch *chronicle.Chronicle, id int64) error {
	k, err := ch.Campaign(id)
	if err != nil {
		return err
	}
	if k.Parent == 0 {
		return nil
	}
	top, err := Top(ch, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("campaign %d %w; halt or resume campaign %d, which holds it", id, ErrChild, top)
}

func state(ch *chronicle.Chronicle, id int64) (chronicle.State, error) {
	if _, err := ch.Campaign(id); err != nil {
		return chronicle.State{}, err
	}
	events, err := ch.Events(id)
	if err != nil {
		return chronicle.State{}, err
	}
	return chronicle.Replay(events), nil
}
