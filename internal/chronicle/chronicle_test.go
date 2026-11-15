package chronicle

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// sqlitePath decodes a "file:" URI the way SQLite does before it hands the
// path to the file system: an optional "//authority" is dropped, the query is
// cut off and %HH sequences are decoded.
func sqlitePath(t *testing.T, uri string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(uri, "file:")
	if !ok {
		t.Fatalf("%q is not a file: URI", uri)
	}
	if strings.HasPrefix(rest, "//") {
		authority, path, _ := strings.Cut(rest[2:], "/")
		if authority != "" && authority != "localhost" {
			t.Fatalf("%q has authority %q, which SQLite rejects", uri, authority)
		}
		rest = "/" + path
	}
	rest, _, _ = strings.Cut(rest, "?")
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '%' && i+2 < len(rest) {
			n, err := strconv.ParseUint(rest[i+1:i+3], 16, 8)
			if err != nil {
				t.Fatalf("%q: bad escape", uri)
			}
			b.WriteByte(byte(n))
			i += 2
			continue
		}
		b.WriteByte(rest[i])
	}
	return b.String()
}

// A failed agent attempt is retried; any other stop is resumed.
func TestStopHint(t *testing.T) {
	failed := []Event{{Kind: CampaignStarted, Node: "a"}, {Kind: StepStarted, Node: "a", Attempt: 1},
		{Kind: StepFinished, Node: "a", Attempt: 1, Details: Details{Failure: FailUnable, Error: "no"}}, {Kind: Stopped, Node: "a", Details: Details{Error: "no"}}}
	if h := StopHint(failed); h != HintRetry {
		t.Errorf("unable: %q", h)
	}
	other := []Event{{Kind: CampaignStarted, Node: "a"}, {Kind: StepStarted, Node: "a", Attempt: 1},
		{Kind: StepFinished, Node: "a", Attempt: 1, Outcome: "odd"}, {Kind: Stopped, Node: "a", Details: Details{Error: "no transition"}}}
	if h := StopHint(other); h != HintResume {
		t.Errorf("no transition: %q", h)
	}
	if h := StopHint(failed[:2]); h != "" {
		t.Errorf("not stopped: %q", h)
	}
}

func TestFileURI(t *testing.T) {
	for _, path := range []string{
		`C:/Users/RUNNER~1/AppData/Local/Temp/hetman-test-2927611789/.hetman/chronicle.db`,
		`D:/a b/c#d/e%f/chronicle.db`,
		`//server/share/dir/chronicle.db`,
		`/home/user/project/.hetman/chronicle.db`,
		`/tmp/odd ?#%/chronicle.db`,
	} {
		uri := fileURI(path) + "?_txlock=immediate"
		if got := sqlitePath(t, uri); got != path {
			t.Errorf("%q: SQLite would open %q, want %q", uri, got, path)
		}
		// The driver reads its own options with net/url.
		u, err := url.Parse(uri)
		if err != nil || u.Query().Get("_txlock") != "immediate" {
			t.Errorf("%q: driver cannot read options: %v", uri, err)
		}
	}
	if got := fileURI(`C:/Users/RUNNER~1/x.db`); got != `file:C:/Users/RUNNER~1/x.db` {
		t.Errorf("drive path: %s", got)
	}
}

func TestOpenPathWithSpecialCharacters(t *testing.T) {
	name := "a b#c%d~1"
	if runtime.GOOS != "windows" {
		name += "?e"
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chronicle.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.NewCampaign("f", "orders.yaml", "a", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not created at the given path: %v", err)
	}
	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Campaign(id); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var mode string
	if err := c.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "truncate" {
		t.Fatalf("journal mode %q (%v), want truncate", mode, err)
	}
}

func TestCampaignsAndLastSeq(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "chronicle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if seq, err := c.LastSeq(); err != nil || seq != 0 {
		t.Fatalf("empty journal: seq %d, %v", seq, err)
	}
	first, _ := c.NewCampaign("f", "orders.yaml", "a", nil, "")
	second, _ := c.NewCampaign("g", "orders.yaml", "a", nil, "")
	seq, _ := c.LastSeq()
	e, err := c.Append(Event{Campaign: first, Kind: StepStarted, Node: "a", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := c.LastSeq(); after != e.Seq || after <= seq {
		t.Fatalf("seq %d after append, before %d, event %d", after, seq, e.Seq)
	}
	list, err := c.Campaigns(10)
	if err != nil || len(list) != 2 || list[0].ID != second || list[0].Flow != "g" || list[1].ID != first {
		t.Fatalf("campaigns %+v, %v", list, err)
	}
	if list, _ := c.Campaigns(1); len(list) != 1 || list[0].ID != second {
		t.Fatalf("limited list %+v", list)
	}
}
