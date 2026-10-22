// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"math"
	"testing"
)

// TestLengthsThatOverflowTheirFieldsAreRefused: a header or payload longer than its 32-bit
// length field can state is refused, not written with a truncated length that misframes every
// PDU after it on the stream.
func TestLengthsThatOverflowTheirFieldsAreRefused(t *testing.T) {
	if err := lengthsFit(MandatoryHeaderLength, math.MaxUint32); err != nil {
		t.Errorf("the largest payload the field can state was refused: %v", err)
	}
	if err := lengthsFit(MandatoryHeaderLength, math.MaxUint32+1); err == nil {
		t.Error("a payload one byte past what its length field can state was accepted")
	}
	if err := lengthsFit(math.MaxUint32+1, 0); err == nil {
		t.Error("a header one byte past what its length field can state was accepted")
	}
}
