package cli

import (
	"flag"
	"fmt"

	"hetman/internal/engine"
	"hetman/internal/i18n"
)

// againFlags are the flags of retry and rewind.
type againFlags struct {
	to *string // rewind: the node to go back to
	by *string
}

func addAgainFlags(p *i18n.Printer, fs *flag.FlagSet, rewind bool) *againFlags {
	f := &againFlags{by: fs.String("by", "", p.T("flag.again_by"))}
	if rewind {
		f.to = fs.String("to", "", p.T("flag.to"))
	}
	return f
}

// again retries the failed step of a campaign, or rewinds it to a node it
// visited, then drives it in this process as resume does.
func (c *command) again(cmd string, id int64, f *againFlags) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	by := *f.by
	if by == "" {
		by = userName()
	}
	var release func()
	node := ""
	if cmd == "retry" {
		release, node, err = engine.Retry(c.o, c.chron, k.ID, by, engine.ViaCommand)
	} else {
		if *f.to == "" {
			fmt.Fprintln(c.stderr, c.p.T("cli.rewind_needs_to"))
			return exitUsage
		}
		node = *f.to
		release, err = engine.Rewind(c.o, c.chron, k.ID, node, by, engine.ViaCommand)
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "hetman %s: %s\n", cmd, c.p.Error(err))
		return exitFailed
	}
	defer release()
	fmt.Fprintln(c.stdout, c.p.T("cli."+cmd+"_done", "id", k.ID, "node", node))
	return c.drive(k.ID)
}
