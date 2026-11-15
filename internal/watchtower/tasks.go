package watchtower

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/dispatch"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// taskLimit is how many tasks the tasks view shows, newest first.
const taskLimit = 200

// queue returns the task queue of the orders folder, opened the first time
// it exists, or nil while the folder has none.
func (s *Server) queue() *dispatch.Queue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks != nil {
		return s.tasks
	}
	path := dispatch.QueuePath(filepath.Dir(s.OrdersPath))
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	q, err := dispatch.OpenQueue(path)
	if err != nil {
		return nil
	}
	s.tasks = q
	return q
}

// Close closes the task queue if the server opened it.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks != nil {
		s.tasks.Close()
		s.tasks = nil
	}
}

// taskStamp changes whenever the queue changes; zero without a queue.
func (s *Server) taskStamp() int64 {
	q := s.queue()
	if q == nil {
		return 0
	}
	n, _ := q.Stamp()
	return n
}

// taskViews returns the rows of the tasks view, newest first; nil when the
// folder has no queue.
func (s *Server) taskViews(p *i18n.Printer) ([]dispatch.View, error) {
	q := s.queue()
	if q == nil {
		return nil, nil
	}
	list, err := q.List(taskLimit)
	if err != nil {
		return nil, err
	}
	views, err := dispatch.Views(list, s.Chron, filepath.Dir(s.OrdersPath), p)
	if err != nil {
		return nil, err
	}
	for i := range views {
		views[i].Input = nil
	}
	return views, nil
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	views, err := s.taskViews(s.printer(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if views == nil {
		views = []dispatch.View{}
	}
	writeJSON(w, http.StatusOK, views)
}

type loaded struct {
	o     *orders.Orders
	stamp time.Time
	size  int64
}

// ordersFor is ordersOf for a campaign id.
func (s *Server) ordersFor(id int64) *orders.Orders {
	k, err := s.Chron.Campaign(id)
	if err != nil {
		o, _ := s.currentOrders()
		return o
	}
	o, _ := s.ordersOf(k)
	return o
}

// ordersOf returns the orders a campaign was started from: the served
// orders, or another orders file of the same folder, such as the flow a
// dispatcher routed a task to. It falls back to the served orders when that
// file is elsewhere or does not load.
func (s *Server) ordersOf(k chronicle.Campaign) (*orders.Orders, string) {
	o, problem := s.currentOrders()
	if k.Orders == "" || k.Orders == o.Path || filepath.Dir(k.Orders) != o.Dir {
		return o, problem
	}
	fi, err := os.Stat(k.Orders)
	if err != nil {
		return o, "the orders file of this campaign is gone, so the served orders are shown: " + err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.others[k.Orders]; ok && c.stamp.Equal(fi.ModTime()) && c.size == fi.Size() {
		return c.o, ""
	}
	other, err := orders.Load(k.Orders)
	if err != nil {
		if c, ok := s.others[k.Orders]; ok {
			return c.o, "the orders file does not load, so the last good version is shown: " + err.Error()
		}
		return o, "the orders file of this campaign does not load, so the served orders are shown: " + err.Error()
	}
	if s.others == nil {
		s.others = map[string]loaded{}
	}
	s.others[k.Orders] = loaded{o: other, stamp: fi.ModTime(), size: fi.Size()}
	return other, ""
}
