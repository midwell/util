// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSender blocks every delivery until release is closed, and counts what it was given.
type blockingSender struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	sent    atomic.Int64
}

func (b *blockingSender) Send(*PDU) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.sent.Add(1)

	return nil
}

func (b *blockingSender) Close() error { return nil }

// TestAPoolCloseThatGivesUpCountsWhatItAbandons: a close that stops waiting for a sender to
// drain leaves that sender's queue to a process about to exit. What was still queued is
// counted as dropped instead of being left to drain unaccounted for.
func TestAPoolCloseThatGivesUpCountsWhatItAbandons(t *testing.T) {
	var drops atomic.Int64

	inner := &blockingSender{release: make(chan struct{}), entered: make(chan struct{})}
	a := NewAsyncSender(inner, 64, nil, func() { drops.Add(1) })

	p := NewPool(nil, KeepaliveConfig{}, nil, func() { drops.Add(1) })
	p.senders["mdf.example:1"] = a

	if err := a.Send(product()); err != nil {
		t.Fatal(err)
	}
	<-inner.entered // the worker holds the first unit, blocked

	for range 10 {
		if err := a.Send(product()); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.Close(); !errors.Is(err, errCloseTimedOut) {
		t.Fatalf("Close() = %v, want the close deadline", err)
	}

	close(inner.release)
	eventually(t, "the abandoned sender's worker to finish", func() bool {
		return inner.sent.Load()+drops.Load() >= 11
	})
	time.Sleep(50 * time.Millisecond)

	if got := drops.Load(); got != 10 {
		t.Errorf("%d of the 10 units still queued when the close gave up were reported dropped; "+
			"the rest were delivered after the closer stopped waiting, or lost uncounted", got)
	}
	if got := inner.sent.Load(); got != 1 {
		t.Errorf("%d units were delivered, want only the one already being written", got)
	}
}
