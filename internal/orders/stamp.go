package orders

import (
	"fmt"
	"os"
	"strings"
)

// A stamp sums up the files of orders (see Files) as their sizes and times
// of change, so that a process holding orders can tell that a file was
// changed since they were read.

// Stamp is the stamp of the files of the orders as they were when Load read
// them: each file is looked at just before it is read, so an edit made while
// the orders were being read shows up as a change later. It is empty for
// orders that were not read from the disk by Load, such as orders checked
// with files not written yet.
func (o *Orders) Stamp() string { return o.loaded }

// StampNow is the stamp of the files of the orders as they are now.
func (o *Orders) StampNow() string { return o.stampFrom(nil) }

// stampFrom makes the stamp of the files of o, taking a file's from seen
// when it is there and from the disk otherwise.
func (o *Orders) stampFrom(seen map[string]string) string {
	var b strings.Builder
	for _, f := range o.Files() {
		s, ok := seen[f]
		if !ok {
			s = fileStamp(f)
		}
		b.WriteString(f)
		b.WriteByte(0)
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d\x00%d", fi.Size(), fi.ModTime().UnixNano())
}

// note keeps the stamp of a file about to be read, the first time it is.
func (p *parser) note(path string) {
	if p.seen == nil {
		return
	}
	if _, ok := p.seen[path]; !ok {
		p.seen[path] = fileStamp(path)
	}
}
