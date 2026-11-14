package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hetman/internal/engine"
	"hetman/internal/watchtower"
)

// DefaultAddr is where serve listens unless told otherwise. Only this
// computer can reach it.
const DefaultAddr = "127.0.0.1:8740"

// serve runs Watchtower until interrupted. Campaigns resumed from the page
// are driven by this process; on interrupt their current step is cut off and
// runs again on the next resume, as with run.
func (c *command) serve(addr string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := engine.ContainChildren(); err != nil {
		fmt.Fprintf(c.stderr, "hetman: step processes are not tied to this process (%v); they are cleaned up on resume instead\n", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return c.fail(err)
	}
	srv := watchtower.New(ctx, c.o, c.chron)
	srv.Log = c.stdout
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		srv.Hosts = []string{host}
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	fmt.Fprintf(c.stdout, "watchtower for %s on http://%s/ (Ctrl+C to stop)\n", c.o.Path, ln.Addr())
	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()
	select {
	case err := <-served:
		return c.fail(err)
	case <-ctx.Done():
	}
	// Open event streams end with the context; give other requests a moment.
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(c.stderr, "hetman:", err)
	}
	srv.Wait()
	fmt.Fprintln(c.stdout, "watchtower stopped")
	return exitInterrupted
}
