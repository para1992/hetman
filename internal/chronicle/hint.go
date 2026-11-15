package chronicle

// Commands a person continues a campaign stopped in the error state with.
const (
	HintRetry  = "retry"
	HintResume = "resume"
)

// StopHint says how to continue a campaign stopped in the error state, or
// "" when it is not stopped. When the step itself failed (an agent attempt
// that ended with a failure, such as cannot_proceed), hetman retry runs it
// again with a fresh attempt budget. When the step's result stands and the
// cause lies outside it (a program that could not start, an outcome without
// a transition, a node no longer in the orders, a missing workdir, an
// adapter that does not describe itself, a child that stopped), the cause
// is fixed and hetman resume continues from where the campaign stopped,
// running the step again only if it had not finished; for a child, resume
// continues the child where it stopped.
func StopHint(events []Event) string {
	st := Replay(events)
	if st.Error == "" {
		return ""
	}
	stop := -1
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == Stopped {
			stop = i
			break
		}
	}
	for i := stop - 1; i >= 0; i-- {
		e := events[i]
		if e.Node != st.Node {
			continue
		}
		switch e.Kind {
		case StepFinished:
			if e.Failure != "" && e.Outcome == "" {
				return HintRetry
			}
			return HintResume
		case StepStarted, Transition, Retried, Rewound, Resumed:
			return HintResume
		}
	}
	return HintResume
}
