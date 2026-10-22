// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"sync"
	"time"
)

// WatchTaskFaults reports both edges of the task-scoped conditions this element can
// re-observe, until stop is closed. It blocks — run it in a goroutine.
//
// TS 103 221-1 clause 6.5.2.1: "If a non-terminating fault is cleared, the NE shall send
// another ReportTaskIssue indicating the fault is cleared." A clearing report needs an owner
// that can see the condition end, and the conditions that can end are the ones the
// WithTaskFaults supplier answers from state — so the watcher samples that supplier for every
// task this element holds, reports a NonTerminatingFault when a state condition appears, and
// an AllClear once it has stayed absent for an interval. The same hold the destination
// watcher applies keeps a condition that flaps from producing a report and a retraction per
// sample.
//
// A fault reported against a task this element no longer holds is forgotten rather than
// retracted: the task's end ends it, and the ADMF has already deactivated the XID an AllClear
// would name.
//
// Events — a record that could not be encoded, for instance — are not the watcher's: they are
// pushed when they happen (NotifyTaskAsync) and have no ending. Without a WithTaskFaults
// supplier there is nothing to sample, and this returns at once. A non-positive interval
// takes the report throttle's.
func (s *Server) WatchTaskFaults(r *Reporter, interval time.Duration, stop <-chan struct{}) {
	if s.taskFaults == nil {
		return
	}
	w := newTaskWatch(s, r, interval)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w.sampleUntil(stop)
		}
	}
}

// taskWatch is WatchTaskFaults' state.
type taskWatch struct {
	srv      *Server
	reporter *Reporter
	interval time.Duration
	// now is the clock the hold is measured on. Monotonic; replaced only by tests.
	now func() time.Time

	// mu guards healthy. It is never held across a report.
	mu sync.Mutex
	// healthy is when each reported fault was first seen absent again; a fault is retracted
	// once it has stayed absent for an interval.
	healthy map[reportKey]time.Time
}

func newTaskWatch(s *Server, r *Reporter, interval time.Duration) *taskWatch {
	if interval <= 0 {
		interval = reportThrottle
	}

	return &taskWatch{
		srv:      s,
		reporter: r,
		interval: interval,
		now:      monotonicNow,
		healthy:  make(map[reportKey]time.Time),
	}
}

// taskReport is one report a sample decided on.
type taskReport struct {
	key     reportKey
	details string
	clear   bool
}

// sampleUntil reports the transitions since the last look, making no further report once stop
// is closed.
func (w *taskWatch) sampleUntil(stop <-chan struct{}) {
	plan := w.plan(w.now())

	for _, p := range plan {
		select {
		case <-stop:
			return
		default:
		}
		if p.clear {
			w.reporter.NotifyTaskClear(p.key.id, p.key.condition)
			w.settled(p.key)

			continue
		}
		w.reporter.NotifyTaskFault(p.key.id, p.key.condition, p.details)
	}
}

// plan decides what this sample reports.
func (w *taskWatch) plan(now time.Time) []taskReport {
	held := make(map[string]bool)
	present := make(map[reportKey]string)

	// The supplier is called without any lock of the watcher's or the server's, as
	// WithTaskFaults documents.
	for _, t := range w.srv.store.Snapshot() {
		held[string(t.XID)] = true
		for _, f := range w.srv.taskFaults(t.XID) {
			condition, details, ok := strings.Cut(f.ErrorDescription, ": ")
			if !ok || !taskStates[condition] {
				continue
			}
			present[reportKey{scope: scopeTask, id: string(t.XID), condition: condition}] = details
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	var plan []taskReport
	for k, details := range present {
		delete(w.healthy, k)
		plan = append(plan, taskReport{key: k, details: details})
	}

	standing := make(map[reportKey]bool)
	for _, k := range w.reporter.standingTaskFaults() {
		standing[k] = true
		if !held[k.id] {
			w.reporter.forget(k)
			delete(w.healthy, k)

			continue
		}
		if _, still := present[k]; still {
			continue
		}
		since, ok := w.healthy[k]
		if !ok {
			w.healthy[k] = now

			continue
		}
		if now.Sub(since) >= w.interval {
			plan = append(plan, taskReport{key: k, clear: true})
		}
	}
	for k := range w.healthy {
		if !standing[k] {
			delete(w.healthy, k)
		}
	}

	return plan
}

// settled forgets the hold of a fault whose retraction arrived. One that did not arrive keeps
// its hold, so the next sample asks again.
func (w *taskWatch) settled(k reportKey) {
	if w.reporter.holds(k) {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.healthy, k)
}
