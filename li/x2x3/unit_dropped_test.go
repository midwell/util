// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// scriptedConn is a net.Conn whose writes answer from write, and whose reads block until it is
// closed — enough for a client to hold it as its live connection.
type scriptedConn struct {
	write  func(b []byte) (int, error)
	once   sync.Once
	closed chan struct{}
}

func newScriptedConn(write func(b []byte) (int, error)) *scriptedConn {
	return &scriptedConn{write: write, closed: make(chan struct{})}
}

func (s *scriptedConn) Write(b []byte) (int, error) { return s.write(b) }

func (s *scriptedConn) Read([]byte) (int, error) {
	<-s.closed

	return 0, net.ErrClosed
}

func (s *scriptedConn) Close() error {
	s.once.Do(func() { close(s.closed) })

	return nil
}

func (*scriptedConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*scriptedConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*scriptedConn) SetDeadline(time.Time) error      { return nil }
func (*scriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (*scriptedConn) SetWriteDeadline(time.Time) error { return nil }

// TestADroppedTailIsCountedAndLeavesTheDestinationReachable drives the one path that knowingly
// loses product, without depending on how much a kernel's socket buffers absorb: the write of a
// three-unit batch stops inside the second unit, the client reconnects, and the resend of the
// second and third units fails on the fresh connection too.
//
// The first unit reached the mediation function, so the destination is reachable and the loss
// is ErrUnitDropped — two units, from the one the write stopped inside to the end of the batch.
// An ordinary error here would mark a working mediation function unreachable.
func TestADroppedTailIsCountedAndLeavesTheDestinationReachable(t *testing.T) {
	pdus := []*PDU{product(), product(), product()}
	first, err := pdus[0].Marshal()
	if err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("connection reset")
	held := newScriptedConn(func([]byte) (int, error) { return len(first) + 5, writeErr })
	fresh := newScriptedConn(func([]byte) (int, error) { return 0, writeErr })

	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	c.dialConn = func(context.Context) (net.Conn, error) { return fresh, nil }
	t.Cleanup(func() { _ = c.Close() })

	c.mu.Lock()
	c.live = &connState{conn: held, stop: make(chan struct{})}
	c.mu.Unlock()

	err = c.SendBatch(pdus)

	if !errors.Is(err, ErrUnitDropped) {
		t.Fatalf("SendBatch() = %v, want ErrUnitDropped: the first unit landed, so this is a "+
			"dropped tail and not a failed delivery", err)
	}
	if got := LostUnits(err); got != 2 {
		t.Errorf("the error counts %d lost unit(s), want 2: the unit the write stopped inside "+
			"and the one after it", got)
	}
	if c.Unreachable() {
		t.Error("a destination that took the first unit was marked unreachable over the tail " +
			"it did not take")
	}
}
