package engine

import (
	"testing"

	"hetman/internal/ask"
)

func TestStopAtAskStopsAtAChildQuestion(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": `version: 0
flow: seg
start: decide
nodes:
  decide: {type: human, on: {go: done, stop: failed}}
`})
	f.e.StopAtAsk = true
	f.drive(Asking)
	asked, _, err := Pending(f.chron, f.e.Campaign)
	if err != nil || asked == nil || asked.Campaign == f.e.Campaign {
		t.Fatalf("pending %+v %v", asked, err)
	}
	if _, err := Answer(f.chron, f.e.Campaign, Reply{Given: ask.Given{Outcome: "go"}}); err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
}
