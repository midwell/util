// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"slices"
	"sync"
	"time"

	"github.com/omec-project/util/li/types"
)

// DestinationHealth is one delivery destination's identity and whether this element
// can currently reach it.
//
// It is the watcher's supplier shape, and it is deliberately *not* the status
// probe's. MDFUnreachableProbe takes counts so that a status answer cannot name a
// destination — an element's own status says how much is wrong, never whose product
// is affected. A destination-scoped report says which, because the provisioning
// function asked about none in particular and that is exactly what it needs to know.
// Same fact, two questions, two shapes.
type DestinationHealth struct {
	// DID is the identifier the provisioning function assigned. An endpoint this
	// element resolved from its own configuration has none, and is not reportable at
	// destination scope because the ADMF has nothing to act on.
	DID string
	// Address is the endpoint the identifier resolves to. Two identifiers may share
	// one, which is why a fault is reported per identifier and not per address.
	Address     string
	Unreachable bool
}

// DestinationHealthOf joins the destinations a set of tasks delivers to with a
// reachability answer per address.
//
// It exists so the join is written once rather than in each network function. The
// suppliers differ — a Pool for the SMF and AMF, its own sender map for the UPF —
// but both answer the same question about an address, so what varies is one
// function argument.
//
// destinations SHALL be the same resolution the element's delivery path uses, and
// is passed in rather than taken as t.DeliveryAddresses(dt) for a reason that cost a
// lost fault report to learn. An IRI-POI delivers a task naming no DID to its own
// configured endpoint, which provisioning never resolved — so the task carries no
// delivery record at all, and a join reading the task alone sees nothing to watch
// and reports nothing about the commonest configuration there is. Asking the caller
// for the addresses it actually delivers to makes the two impossible to disagree.
//
// Deduplicated by (identifier, address): a destination named by several tasks is one
// destination, and reporting it once per task would tell the provisioning function
// about its own tasking rather than about its endpoint.
func DestinationHealthOf(tasks []types.InterceptTask, dt types.DeliveryType,
	destinations func(types.InterceptTask) []string,
	unreachable func(addr string) bool,
) []DestinationHealth {
	var out []DestinationHealth

	for _, t := range tasks {
		for _, addr := range destinations(t) {
			dids := t.DeliveryDIDs(dt, addr)
			if len(dids) == 0 {
				// An address this element delivers to that provisioning did not name:
				// its own configured endpoint. There is nothing destination-scoped to
				// say about it, and still something to say — reported at element scope
				// by the empty identifier, which is what the watcher keys the scope on.
				dids = []string{""}
			}
			for _, did := range dids {
				if slices.ContainsFunc(out, func(h DestinationHealth) bool {
					return h.DID == did && h.Address == addr
				}) {
					continue
				}
				out = append(out, DestinationHealth{
					DID: did, Address: addr, Unreachable: unreachable(addr),
				})
			}
		}
	}

	return out
}

// DestinationWatcher reports both edges of a delivery destination's reachability:
// that it has become unreachable, and that it has recovered.
//
// **One owner for both edges, and that is the point.** Reachability is re-observable
// — every supplier answers from state its senders already hold — so an ending is
// detectable, which is what makes a clearing report possible at all. Nothing in the
// delivery layer signals recovery, and adding a callback beside the failure one
// would put edge detection at five sites across three network functions, each free
// to disagree about what "recovered" means. An element where one party announces and
// another retracts eventually announces something nobody retracts.
//
// What it deliberately does not cover: a fault the element cannot re-observe. A
// destination that could not be *prepared* is a credential or configuration fault
// rather than a reachability one, and a fault that cannot be observed to hold cannot
// be observed to end. Those keep being reported where they are noticed, at element
// scope, and are never cleared — which is the existing event-versus-condition rule
// applied rather than a second mechanism.
type DestinationWatcher struct {
	health   func() []DestinationHealth
	reporter *Reporter
	interval time.Duration
	// condition is the issue name a fault is reported and retracted under, so the
	// two halves cannot drift apart.
	condition string
	// nudge lets a site that has just seen a delivery fail ask for a sample now
	// rather than at the next tick. Buffered and never blocking: it is called from
	// the delivery path, which must not wait on reporting.
	nudge chan struct{}
	// now is the clock the hold below is measured on. Monotonic; replaced only by tests.
	now func() time.Time

	// mu guards the record below. Watch samples on one goroutine, but sample is also
	// reached directly.
	mu sync.Mutex
	// raised is every destination whose fault this watcher has reported and not yet seen
	// retracted, with the instant it was first seen healthy again — zero while it is not.
	//
	// **It is what lets a fault end when its destination stops being sampled.** The
	// supplier lists the destinations the live tasking refers to, so deactivating a
	// destination's last task, or a purge, takes it out of every later sample — and a
	// fault that is only ever retracted when a sample shows it healthy then stood at the
	// ADMF until the identifier happened to be reused. Clause 5.3: "The NE shall also
	// indicate that a fault has been cleared."
	//
	// And it is the hold: a destination that recovers is retracted only once it has stayed
	// healthy for an interval. Reachability is the last exchange's outcome, with no memory,
	// and each failed delivery nudges a sample — so without the hold a destination failing
	// every other send reported a fault and its retraction per failure, as fast as the
	// ADMF answered. A fault is still reported at once; only its ending waits.
	raised map[string]time.Time
	// elementRaised and elementHealthy are the same record for the element-scope fault
	// that stands for every destination with no provisioned identifier.
	elementRaised  bool
	elementHealthy time.Time
}

// Nudge asks for a sample at the next opportunity.
//
// It exists so that moving the fault report off the delivery path did not make the
// report *late*. The sites that used to report a failure the moment they saw it now
// call this instead: the watcher still decides whether anything is a transition —
// one owner for both edges — but it gets to decide immediately rather than up to an
// interval later. Without it, an ADMF would learn of a failed destination one
// sampling interval after the element did, which would be a plain regression
// dressed up as a refactor.
//
// Never blocks and never fails. A nudge that arrives while one is already pending
// is dropped, because two nudges and one imply the same work.
func (w *DestinationWatcher) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// NewDestinationWatcher returns a watcher over the destinations health reports.
//
// health must not perform I/O: it is called on a timer, and a supplier that grew a
// network call would put delivery latency on a schedule inside the LI plane. Every
// supplier this package is used with answers from state each sender already holds,
// which is the same property MDFUnreachableProbe documents for its own.
func NewDestinationWatcher(health func() []DestinationHealth, reporter *Reporter, interval time.Duration) *DestinationWatcher {
	if interval <= 0 {
		interval = reportThrottle
	}

	return &DestinationWatcher{
		health:    health,
		reporter:  reporter,
		interval:  interval,
		condition: NEIssueMDFUnreachable,
		nudge:     make(chan struct{}, 1),
		now:       monotonicNow,
		raised:    make(map[string]time.Time),
	}
}

// Watch samples until stop is closed. A nil stop channel runs for the life of the
// process, which is the right lifetime for an element that can hold tasking.
func (w *DestinationWatcher) Watch(stop <-chan struct{}) {
	if w == nil || w.health == nil {
		return
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w.sample()
		case <-w.nudge:
			// A site has just seen a destination fail. Sampling now keeps the report as
			// prompt as it was when the site made it itself; the decision about whether
			// this is a transition still belongs here.
			w.sample()
		}
	}
}

// sample reports the transitions since the last look.
//
// Both directions go through the reporter's own record of what it has told the
// provisioning function, so a destination that is unreachable at every sample is
// reported once per throttle window rather than once per sample, and one that
// recovers without ever having been reported produces nothing.
func (w *DestinationWatcher) sample() {
	now := w.now()

	w.mu.Lock()
	defer w.mu.Unlock()

	// **The destinations with no provisioned identifier are one element state, not
	// several**, and that is a property of the scope rather than a convenience.
	//
	// An element-scoped report names no destination by construction — MDFUnreachableProbe
	// takes counts precisely so the answer *cannot* name one — so reporting each of them
	// separately would send the ADMF several reports carrying identical text about
	// endpoints it cannot tell apart, and each one's retraction would retract the others.
	// Reported per entry, with one such endpoint down and one healthy, whichever the
	// iteration reached last decided whether the fault stood: the defect was intermittent
	// because Go's map order decided it.
	//
	// What the element can honestly say at this scope is the aggregate: a fault while any
	// un-identified destination is unreachable, and a clear only when none is.
	var (
		unnamed     bool
		unnamedDown bool
		sampled     = make(map[string]bool)
	)

	for _, h := range w.health() {
		// An endpoint this element resolved from its own configuration has no
		// identifier the provisioning function assigned, so there is nothing
		// *destination-scoped* to say about it — but there is still something to
		// say. It is a delivery this element cannot make, which is what the
		// network-element-level condition has always been for, and saying nothing
		// would lose a report that was being made before this watcher existed.
		//
		// Both edges either way. The scope changes with what can be named; whether
		// the fault is reported at all does not.
		if h.DID == "" {
			unnamed = true
			unnamedDown = unnamedDown || h.Unreachable

			continue
		}
		sampled[h.DID] = true

		if h.Unreachable {
			w.reporter.NotifyDestinationFault(h.DID, w.condition,
				"delivery destination is unreachable")
			w.raised[h.DID] = time.Time{}

			continue
		}

		healthySince, ours := w.raised[h.DID]
		if !ours {
			// Not a fault this watcher raised: a retraction is still asked for, and the
			// reporter answers it only if something reported one.
			w.reporter.NotifyDestinationClear(h.DID, w.condition)

			continue
		}
		if healthySince.IsZero() {
			w.raised[h.DID] = now

			continue
		}
		if now.Sub(healthySince) >= w.interval {
			w.retractDestination(h.DID)
		}
	}

	// A destination that has left the health set is no longer tasked, so there is no
	// recovery to wait for: its fault is retracted now.
	for did := range w.raised {
		if !sampled[did] {
			w.retractDestination(did)
		}
	}

	w.sampleElement(now, unnamed, unnamedDown)
}

// retractDestination asks for a raised destination fault to be retracted, and forgets it
// once the reporter no longer holds it. A retraction that did not arrive keeps the entry,
// so the next sample asks again.
func (w *DestinationWatcher) retractDestination(did string) {
	w.reporter.NotifyDestinationClear(did, w.condition)

	if !w.reporter.holds(reportKey{scope: scopeDestination, id: did, condition: w.condition}) {
		delete(w.raised, did)
	}
}

// sampleElement is sample's element-scope half, for the destinations with no provisioned
// identifier.
func (w *DestinationWatcher) sampleElement(now time.Time, unnamed, unnamedDown bool) {
	if unnamedDown {
		w.reporter.NotifyElementFault(w.condition, "a delivery destination is unreachable")
		w.elementRaised, w.elementHealthy = true, time.Time{}

		return
	}

	if !w.elementRaised {
		// Only where this element has such a destination at all: an element with none has
		// nothing to say at this scope, and saying "all clear" would retract a fault raised
		// by whatever else uses the same condition.
		if unnamed {
			w.reporter.NotifyElementClear(w.condition)
		}

		return
	}

	// This watcher's own fault. While un-identified destinations remain, it is retracted
	// once they have stayed healthy for an interval; when none remains, at once, because
	// there is nothing left to recover.
	if unnamed {
		if w.elementHealthy.IsZero() {
			w.elementHealthy = now

			return
		}
		if now.Sub(w.elementHealthy) < w.interval {
			return
		}
	}

	w.reporter.NotifyElementClear(w.condition)

	if !w.reporter.holds(reportKey{scope: scopeElement, condition: w.condition}) {
		w.elementRaised, w.elementHealthy = false, time.Time{}
	}
}
