// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNilPDU is what every delivery path reports for a product unit that is not there:
// SendBatch for a nil entry, Send and AsyncSender.Send for a nil argument, and Marshal for
// a nil receiver. Nothing was delivered for it, which is the same loss a PDU that fails to
// marshal represents.
//
// One error rather than one per path, and exported, because a POI's own delivery wrapper has
// to be able to tell it from a transport failure: this one says the element built no
// product, which is a defect on this side, and the other says the mediation function did
// not take it.
var ErrNilPDU = errors.New("x2x3: nil product unit")

// Client delivers X2 (xIRI) and X3 (xCC) PDUs to a Mediation & Delivery
// Function over a single TLS-secured TCP connection (ETSI TS 103 221-2). It
// connects lazily on the first Send and reconnects once on a write failure.
// It is safe for concurrent use.
//
// The caller supplies the *tls.Config — for LI this is mtls.ClientTLS(), which
// presents the network element's LI certificate and verifies the MDF.
type Client struct {
	addr         string
	tlsConfig    *tls.Config
	dialTimeout  time.Duration
	writeTimeout time.Duration
	keepalive    KeepaliveConfig

	mu sync.Mutex
	// live is the connection currently held, with the keepalive state that belongs
	// to it, or nil when none is. Everything about one connection dies with it —
	// see connState.
	live *connState
	// closed records that Close was called, which deliver has to know: it treats a nil
	// live as "not connected yet" and dials. Without this a Send after Close delivered
	// product over a *new* connection to a destination this element had finished with —
	// AsyncSender guards that for every pooled caller today, and a client should not
	// depend on its wrapper for a property of its own lifecycle.
	closed bool

	// lastDialFailure and lastDialErr record the most recent dial that failed, and are
	// cleared by one that succeeds. Guarded by mu. They are what lets deliver refuse to
	// dial again within redialInterval — see minRedialInterval.
	lastDialFailure time.Time
	lastDialErr     error
	// redialInterval is minRedialInterval, held per client so a test can shorten it.
	redialInterval time.Duration
	// lastProbe is when the most recent redial after a peer's close was started — see
	// probeAfterPeerClose. Guarded by mu.
	lastProbe time.Time
	// probing is whether a post-close probe is in flight; at most one is. Guarded by mu.
	probing bool

	// lifetime is cancelled by Close. It bounds the dials this client makes, so a Close
	// does not wait out a dial's timeout, and it is what a probe waiting out its backoff
	// watches for.
	lifetime context.Context
	cancel   context.CancelFunc
	// probes counts the post-close probes in flight, which belong to no connection and so
	// are not covered by a connection's own wait group. Close waits for them.
	probes sync.WaitGroup

	// dialConn replaces the TLS dial when set, so a test can hand the client a connection
	// whose writes fail in a chosen way. Set only by tests; nil otherwise.
	dialConn func(ctx context.Context) (net.Conn, error)

	// unreachable is what the most recent exchange with this destination established,
	// kept outside mu because a fault probe reads it on the X1 request goroutine: mu is
	// held across a dial and a write, and an answer to a provisioning function must not
	// wait for either. See Unreachable.
	unreachable atomic.Bool
}

// minRedialInterval is how long a client waits after a failed dial before it dials the same
// destination again. Deliveries offered in between fail at once, without a dial, and are
// accounted as lost like any other failed delivery.
//
// **Without it an unreachable mediation function costs a dial per batch.** A refusing
// destination fails each dial in microseconds, so the delivery worker dialled — TCP and a TLS
// handshake attempt — as fast as product arrived, and reported an error for each: the rate of
// both tracked the offered load, which on X3 is the subject's packet rate, not any retry
// schedule.
//
// One second bounds that to one dial per destination per second. It is the resolution TS 103
// 221-2 clause 6.2.4 expresses its own connection timers in, and it is short against how long
// an outage that refuses connections lasts. What it costs is product offered in the second
// after a failed dial, which is refused without an attempt — product a dial would almost
// certainly have lost as well, since the last one a moment ago failed.
//
// The redial that follows a write failure on a held connection is not subject to it: that
// connection was working a moment ago, and the one reconnect deliver makes is how an idle
// connection the mediation function dropped is restored.
const minRedialInterval = time.Second

// DeliveryError is a delivery failure together with how many product units it cost.
//
// **A failed delivery is counted in units, not in calls.** The delivery worker writes up to
// maxDeliveryBatch units at once, and a batch that fails used to reach the network function
// as one error, so an element whose mediation function was refusing connections reported a
// few dozen errors while losing a thousand units — and nothing in its own account could
// recover the number the mediation function would see missing from its sequence. Every error
// the delivery worker reports carries the count; LostUnits reads it.
//
// It wraps the failure, so errors.Is still finds ErrUnitDropped and ErrNilPDU through it.
type DeliveryError struct {
	// Lost is how many product units were not delivered.
	Lost int
	Err  error
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("%v (%d product unit(s) not delivered)", e.Err, e.Lost)
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// LostUnits reports how many product units err says were not delivered, or 0 when err
// carries no count — nil, or an error from outside a delivery.
func LostUnits(err error) int {
	var de *DeliveryError
	if errors.As(err, &de) {
		return de.Lost
	}

	return 0
}

// lostUnits wraps err with the number of units it cost. A nil err stays nil.
func lostUnits(n int, err error) error {
	if err == nil {
		return nil
	}

	return &DeliveryError{Lost: n, Err: err}
}

// moreLost adds n to the units err already accounts for.
func moreLost(err error, n int) error {
	var de *DeliveryError
	if errors.As(err, &de) {
		return &DeliveryError{Lost: de.Lost + n, Err: de.Err}
	}

	return lostUnits(n, err)
}

// NewClient returns a delivery client for the MDF at addr ("host:port").
//
// The zero KeepaliveConfig is the conformant one — the clause 6.2.4 mechanism, at the
// specification's own timers — so a caller that has no opinion gets the behaviour the
// specification requires rather than none of it.
func NewClient(addr string, tlsConfig *tls.Config, keepalive KeepaliveConfig) *Client {
	lifetime, cancel := context.WithCancel(context.Background())

	return &Client{
		addr:         addr,
		tlsConfig:    tlsConfig,
		dialTimeout:  10 * time.Second,
		writeTimeout: 5 * time.Second,
		keepalive:    keepalive.withDefaults(),

		redialInterval: minRedialInterval,
		lifetime:       lifetime,
		cancel:         cancel,
	}
}

// Send marshals pdu and writes it to the MDF, (re)connecting as needed. A PDU
// is self-delimiting (its header carries the header and payload lengths), so no
// extra framing is added on the wire.
//
// A nil pdu returns ErrNilPDU and dials nothing. Marshal refuses it too — that is where the
// property is enforced for every path — and this is the same answer stated at the entry
// point a POI actually calls, so the error a caller gets does not depend on how far in the
// nil travelled.
func (c *Client) Send(pdu *PDU) error {
	if pdu == nil {
		return ErrNilPDU
	}

	b, err := pdu.Marshal()
	if err != nil {
		return lostUnits(1, err)
	}

	return c.sendBytes(b, nil)
}

// sendBytes writes already-marshalled PDU bytes and records what the attempt established
// about the destination. One place records it, so the answer Unreachable gives cannot
// disagree with the outcome the caller saw.
// boundaries are the offsets at which each product unit in b ends, so a retry after a
// partial write can resume at one. Nil means b is a single unit.
func (c *Client) sendBytes(b []byte, boundaries []int) error {
	err := c.deliver(b, boundaries)

	// A dropped unit is not a statement about the destination. The rest of the batch
	// reached it, so it is reachable, and recording it as unreachable would report a
	// fault about a working mediation function — see ErrUnitDropped. The loss is still
	// returned to the caller.
	c.unreachable.Store(err != nil && !errors.Is(err, ErrUnitDropped))

	return err
}

// Unreachable reports whether the most recent exchange with this destination failed and
// none has since succeeded.
//
// It answers from what that exchange already established and dials nothing. A POI consults
// it from a fault probe, which runs on the X1 request goroutine: a probe that performed I/O
// would hold up a provisioning function's answer and — with a short enough timeout at the
// other end — could make a working element look dead while it asked itself a slow question.
//
// "Exchange" rather than "delivery attempt", since the keepalive mechanism: a connection
// whose mediation function stops acknowledging is reported within TIME_P2 even though
// nothing has been delivered over it, and one whose mediation function closes it is redialled
// at once and reported if that dial fails (see peerClosed). That is the improvement keepalive
// hands this probe — a destination that dies while idle used to be invisible until the next
// send. With the mechanism disabled it still is.
//
// It is still false before anything has been sent, deliberately, and the mechanism does not
// change that: an element with nothing to deliver has not found its mediation function
// unreachable, it has not looked, and keepalives run only on a connection that a delivery
// has already dialled.
func (c *Client) Unreachable() bool {
	return c.unreachable.Load()
}

// deliver writes already-marshalled PDU bytes, reconnecting once if the MDF has
// dropped an idle connection. Reached from both Send and SendBatch through sendBytes, so
// both get the same reconnect behaviour — a batch is only a longer write.
//
// Every failure it returns is a *DeliveryError counting the units that did not arrive: all of
// them when nothing could be written, and only the ones from the resumption point on when a
// write stopped part-way and the retry failed.
func (c *Client) deliver(b []byte, boundaries []int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	units := len(boundaries)
	if units == 0 {
		units = 1
	}

	if c.closed {
		return lostUnits(units, fmt.Errorf("x2x3: send to %s after Close", c.addr))
	}
	if c.live == nil {
		if since := time.Since(c.lastDialFailure); c.lastDialErr != nil && since < c.redialInterval {
			return lostUnits(units, fmt.Errorf("x2x3: not redialling %s for another %s after a failed dial: %w",
				c.addr, (c.redialInterval-since).Round(time.Millisecond), c.lastDialErr))
		}
		if err := c.dialLocked(); err != nil {
			return lostUnits(units, err)
		}
	}

	n, err := c.writeLocked(b)
	if err == nil {
		return nil
	}

	// **The retry resumes at a product-unit boundary, and does not restart the write.**
	// PDUs are self-delimiting and concatenated, so a batch shares one write and a write
	// can end in the middle of one. Sending the whole buffer again after a partial write
	// delivers the leading complete PDUs a second time — on a fresh connection, where
	// nothing marks them as repeats — which is duplicate product under one warrant, the
	// outcome deduplicating destinations by address exists to prevent, arrived at from
	// the delivery mechanism instead of the destination list.
	//
	// **A unit the write stopped inside is resumed from its own start, not skipped.** It
	// used to be dropped, on the reasoning that a receiver reading the head of a stream
	// would take the tail of a unit as the start of one. That is true of resuming mid-unit
	// on the *same* stream, and false of the fresh connection the retry actually goes out
	// on: a TCP connection is its own stream and this one begins at a frame boundary, so a
	// whole unit sent on it cannot be read as anybody's tail. Nor can it duplicate: a unit
	// the kernel did not accept in full is one the peer cannot have received in full,
	// because a write reports what it took and never more.
	//
	// So the loss the drop represented is recovered where the retry lands, and the leading
	// complete units are still not resent.
	rest, resuming := resumeAt(b, boundaries, n)
	// Whether anything reached the old connection whole, which is what makes a later
	// failure a partial success rather than a total one.
	landed := len(rest) < len(b)
	// The units still to deliver: everything from the resumption point on.
	pending := unitsIn(boundaries, len(b), len(rest))

	// One reconnect attempt — the MDF may have dropped an idle connection.
	c.dropLocked()
	if err := c.dialLocked(); err != nil {
		return lostUnits(pending, err)
	}
	if len(rest) > 0 {
		if _, err := c.writeLocked(rest); err != nil {
			c.dropLocked()

			if resuming && landed {
				// The one place units are still knowingly lost: this element had one
				// reconnect, spent it, and the resend did not land. Earlier units did, so
				// the statement is the one ErrUnitDropped is for — delivery succeeded
				// except for the units from the one the write stopped inside to the end of
				// the batch — and the destination is not called unreachable over it,
				// because it took the rest. A drop leaves a gap in a numbered sequence,
				// which the peer can see and the fault channel can explain.
				return lostUnits(pending, fmt.Errorf("%w: send to %s: %v", ErrUnitDropped, c.addr, err))
			}

			return lostUnits(pending, fmt.Errorf("x2x3: send to %s: %w", c.addr, err))
		}
	}

	return nil
}

// unitsIn counts the product units in the last restLen bytes of a total-byte buffer whose
// units end at boundaries. A nil boundaries is one unit, which is in the tail if the tail is
// not empty. The tail always starts at a unit boundary — resumeAt only resumes at one.
func unitsIn(boundaries []int, total, restLen int) int {
	if restLen == 0 {
		return 0
	}
	if len(boundaries) == 0 {
		return 1
	}

	start := total - restLen
	n := 0
	for _, end := range boundaries {
		if end > start {
			n++
		}
	}

	return n
}

// ErrUnitDropped reports that delivery succeeded except for the tail of a batch: the unit a
// write stopped inside, and every unit after it, which were resent on a fresh connection that
// did not take them either. How many is the DeliveryError count that carries it.
//
// It is a distinct error because the two outcomes call for different reports and this
// element must not conflate them. A destination that took the rest of the batch is
// *reachable*; what happened is that product was lost. Returning an ordinary error here
// made sendBytes mark the destination unreachable, which is the same mistake the drop
// hooks exist to avoid — mdfUnreachable and the delivery-lost conditions are deliberately
// separate, because a reachability probe answers "can this element deliver at all" and a
// loss answers "something did not arrive". A watcher told the destination is down would
// report a fault about a mediation function that is working, and retract it on the next
// successful send.
var ErrUnitDropped = errors.New("x2x3: the tail of a partially written batch could not be resent and was dropped")

// resumeAt splits a buffer that was partially written at the first product-unit
// boundary at or after what the peer received, and reports whether a unit was left
// half-delivered.
//
// boundaries are the offsets at which each unit *ends*, in order. A nil boundaries —
// a single-PDU write — means the whole buffer is one unit: anything short of all of it
// is a partial unit, and there is nothing to resume.
func resumeAt(b []byte, boundaries []int, written int) (rest []byte, resuming bool) {
	if written <= 0 {
		return b, false
	}
	if written >= len(b) {
		return nil, false
	}
	if len(boundaries) == 0 {
		// One unit, partially written. It resumes from its own start on the fresh
		// connection: nothing of it was accepted in full, so nothing can be duplicated.
		return b, true
	}

	start := 0
	for _, end := range boundaries {
		if end == written {
			// The peer received whole units and nothing more: resume exactly here.
			return b[written:], false
		}
		if end > written {
			// The write stopped inside this unit. Everything before it was accepted whole
			// and is not resent; this one was not, so it goes out again from its own
			// start — where the old code skipped to b[end:] and lost it.
			return b[start:], true
		}
		start = end
	}

	return nil, false
}

func (c *Client) dialLocked() error {
	conn, err := c.dial()
	if err != nil {
		c.lastDialFailure, c.lastDialErr = time.Now(), err

		return err
	}
	c.installLocked(conn)

	return nil
}

// dial opens a connection to the mediation function without touching the client's state,
// so a caller that must not hold mu across it — the post-close probe — can dial and decide
// under the lock afterwards whether the connection is still wanted.
func (c *Client) dial() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(c.lifetime, c.dialTimeout)
	defer cancel()

	if c.dialConn != nil {
		return c.dialConn(ctx)
	}

	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: c.tlsConfig}
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("x2x3: dial %s: %w", c.addr, err)
	}

	return conn, nil
}

// installLocked makes conn the held connection and starts what runs on its behalf. The
// caller holds mu and has established that no connection is held.
func (c *Client) installLocked(conn net.Conn) {
	c.lastDialFailure, c.lastDialErr = time.Time{}, nil
	c.live = &connState{conn: conn, stop: make(chan struct{})}

	// The keepalive timer and the read path belong to this connection and to no
	// other: they start here and stop in dropLocked, which is what makes a stale
	// reader impossible and a keepalive on a connection nobody holds impossible.
	c.startKeepaliveLocked(c.live)
}

func (c *Client) writeLocked(b []byte) (int, error) {
	// Bound the write so a stalled/half-open MDF cannot block delivery (and every
	// other Send behind the mutex) indefinitely; a timeout is treated as any other
	// write error — drop + one redial.
	if c.writeTimeout > 0 {
		//nolint:errcheck // a deadline a connection will not take is not actionable here
		_ = c.conn().SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	// n is returned rather than discarded: it is what the peer actually received, and
	// therefore what a retry must not send again.
	n, err := c.conn().Write(b)

	return n, err
}

// conn is the held connection. Callers hold mu, and every one of them has already
// established that a connection exists.
func (c *Client) conn() net.Conn { return c.live.conn }

// dropLocked closes the held connection and signals whatever runs on its behalf to
// stop. It does not wait for those goroutines, deliberately: the keepalive timer
// takes mu to write, so waiting here — under mu — would deadlock against the very
// goroutine being waited for. They observe the closed connection or the closed stop
// channel and exit on their own, and nothing they do afterwards can touch the
// client, because each checks that it is still the live connection first.
func (c *Client) dropLocked() {
	if c.live == nil {
		return
	}
	_ = c.live.conn.Close()
	close(c.live.stop)
	c.live = nil
}

// Close closes the underlying connection, if any, and waits for its keepalive timer
// and reader to exit, and for a post-close probe — so a caller that has closed a client
// is not left with goroutines it cannot see. A probe mid-dial is cancelled rather than
// waited out. The wait happens after mu is released, for the reason dropLocked gives.
func (c *Client) Close() error {
	// Cancelled before mu is taken: a delivery dialling under mu would otherwise hold Close
	// behind its dial timeout, which is what lifetime exists to prevent.
	c.cancel()

	c.mu.Lock()
	c.closed = true
	live := c.live
	c.live = nil

	var err error
	if live != nil {
		err = live.conn.Close()
		close(live.stop)
	}
	c.mu.Unlock()

	if live != nil {
		live.wg.Wait()
	}
	c.probes.Wait()

	return err
}

// SendBatch marshals several PDUs and writes them with a single call. PDUs are
// self-delimiting, so concatenating them needs no additional framing, and the
// receiver reads them exactly as it would separate writes. Fewer syscalls and
// fuller TLS records matter when a heavy target's content is the thing being
// delivered.
//
// A PDU that cannot be marshalled is skipped rather than discarding the batch
// around it, but the failure is returned once the rest has been sent: intercept
// product was lost, and losing it quietly is the one outcome this plane may not
// have. A delivery failure takes precedence, being the larger loss.
//
// A nil entry is treated the same way, and for the same reason it is worth a
// branch at all: this is exported, it dereferences a slice the caller owns, and a
// nil there would fault the network function carrying the delivery rather than
// the mediation function it is delivering to.
func (c *Client) SendBatch(pdus []*PDU) error {
	if len(pdus) == 0 {
		return nil
	}

	var (
		buf []byte
		// ends is where each PDU in buf finishes. It is what lets a retry after a
		// partial write resume at a unit boundary rather than resending the whole
		// buffer, so the destination is not sent the leading PDUs twice.
		ends       []int
		marshalErr error
		// skipped counts the units that never reached the buffer, which the error
		// returned counts as lost alongside whatever delivery loses.
		skipped int
	)

	for _, pdu := range pdus {
		if pdu == nil {
			if marshalErr == nil {
				marshalErr = ErrNilPDU
			}
			skipped++

			continue
		}

		b, err := pdu.Marshal()
		if err != nil {
			if marshalErr == nil {
				marshalErr = err
			}
			skipped++

			continue
		}

		buf = append(buf, b...)
		ends = append(ends, len(buf))
	}

	if len(buf) == 0 {
		return lostUnits(skipped, marshalErr)
	}

	if err := c.sendBytes(buf, ends); err != nil {
		if skipped > 0 {
			return moreLost(err, skipped)
		}

		return err
	}

	return lostUnits(skipped, marshalErr)
}
