// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"net"
	"sync/atomic"
	"testing"
)

// TestAStaleExpiryReportsNothing: a TIME_P2 deadline that fires for a connection this client
// has already replaced says nothing about the replacement. It must neither mark the
// destination unreachable nor report a fault.
func TestAStaleExpiryReportsNothing(t *testing.T) {
	m := startMDF(t, answerCorrectly)

	var faults atomic.Int64
	c := clientTo(t, m.addr, KeepaliveConfig{OnFault: func(error) { faults.Add(1) }})

	if err := c.Send(product()); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	stale := &connState{conn: a, stop: make(chan struct{})}

	c.expire(stale)

	if c.Unreachable() {
		t.Error("a deadline on a replaced connection marked a destination reachable over its live " +
			"connection as unreachable")
	}
	if n := faults.Load(); n != 0 {
		t.Errorf("a deadline on a replaced connection reported %d fault(s) about a mediation "+
			"function that is answering", n)
	}

	c.mu.Lock()
	live := c.live != nil
	c.mu.Unlock()

	if !live {
		t.Error("a deadline on a replaced connection dropped the live one")
	}
}
