// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"slices"
	"testing"
)

// Every criterion TS 33.128 table 6.2.3-7 requires must be a distinct type that
// survives a round trip through a task, and must be distinguishable from the
// PFCP Session ID that was the only one supported before. A duplicated constant
// value would silently conflate two criteria and intercept the wrong traffic.
func TestTableCriteriaAreDistinct(t *testing.T) {
	criteria := []TargetIdentifierType{
		TargetFTEID, TargetUEIPv4, TargetUEIPv6, TargetTCPPort, TargetUDPPort,
		TargetPDRID, TargetQERID, TargetNetworkInstance, TargetGTPTunnelDirection,
		TargetPDR, TargetFSEID,
	}
	if len(criteria) != 11 {
		t.Fatalf("expected the nine table types plus the two address forms, got %d", len(criteria))
	}

	seen := make(map[TargetIdentifierType]bool, len(criteria))
	for _, c := range criteria {
		if c == "" {
			t.Error("a criterion has an empty value, so it cannot be distinguished on the wire")
		}
		if seen[c] {
			t.Errorf("criterion %q is duplicated: two criteria sharing a value would select each other's traffic", c)
		}
		seen[c] = true

		if !c.IsPacketCriterion() {
			t.Errorf("%q names traffic and must be usable on LI_T3", c)
		}
	}
}

// subscriberIdentifiers are the identity types an IRI-POI matches a target by. Two
// separate properties are asserted against this one list, so adding a type means
// updating one place rather than remembering two.
var subscriberIdentifiers = []TargetIdentifierType{TargetSUPI, TargetPEI, TargetGPSI}

// Subscriber identifiers must not be mistaken for packet-detection criteria: the
// CC-POI has no way to evaluate them, and the IRI-POIs must keep matching on them.
func TestSubscriberIdentifiersAreNotPacketCriteria(t *testing.T) {
	for _, c := range subscriberIdentifiers {
		if c.IsPacketCriterion() {
			t.Errorf("%q is a subscriber identifier, not a packet-detection criterion", c)
		}
	}
}

// And every one of them must render, because an xIRI reports the identity it matched
// as a conditional attribute TS 33.128 table 5.3.2-2 marks required.
//
// XMLFragments drops what it cannot render rather than substituting something else,
// which is the right rule — but it means a subscriber identity type with no element
// name would cost every xIRI matched on it a mandatory attribute, with nothing
// failing and nothing logged. That is the failure this test exists to make loud, and
// it is here rather than in a POI because here is where the type would be added.
func TestEverySubscriberIdentifierRenders(t *testing.T) {
	// A value of the shape each type's own element takes: a PEI is fourteen or sixteen
	// digits, and one that is neither fits no element and rightly renders as nothing.
	sample := map[TargetIdentifierType]string{TargetSUPI: "262019876543210", TargetPEI: "35784005123456"}
	for _, c := range subscriberIdentifiers {
		v, ok := sample[c]
		if !ok {
			v = "1234567890"
		}
		id := TargetIdentifier{Type: c, Value: v}
		frag, ok := id.XMLFragment()
		if !ok {
			t.Errorf("%q renders no XML fragment, so an xIRI matched on it would carry no matched target identifier", c)

			continue
		}
		if frag == "" {
			t.Errorf("%q renders an empty fragment", c)
		}
	}
}

// TestTargetsAnyMatchesEachCriterionByValue: a task naming a criterion matches an entity
// holding exactly that criterion and nothing else, so the comparison is by type and value.
// The network instance is the schema's hexBinary — "internet" hex-encoded — not a name.
func TestTargetsAnyMatchesEachCriterionByValue(t *testing.T) {
	for _, tc := range []struct {
		typ        TargetIdentifierType
		val, other string
	}{
		{TargetFTEID, "1234@192.0.2.1", "1234@192.0.2.2"},
		{TargetUEIPv4, "192.0.2.50", "192.0.2.51"},
		{TargetUEIPv6, "2001:0db8:0000:0000:0000:0000:0000:0001", "2001:0db8:0000:0000:0000:0000:0000:0002"},
		{TargetTCPPort, "443", "444"},
		{TargetUDPPort, "2152", "2153"},
		{TargetPDRID, "7", "8"},
		{TargetQERID, "3", "4"},
		{TargetNetworkInstance, "696e7465726e6574", "696d73"},
		{TargetGTPTunnelDirection, "Outbound", "Inbound"},
		{TargetPDR, "0a0b0c", "0a0b0d"},
	} {
		task := InterceptTask{Targets: []TargetIdentifier{{Type: tc.typ, Value: tc.val}}}

		if !task.TargetsAny([]TargetIdentifier{{Type: tc.typ, Value: tc.val}}) {
			t.Errorf("%s %q does not match itself", tc.typ, tc.val)
		}
		if task.TargetsAny([]TargetIdentifier{{Type: tc.typ, Value: tc.other}}) {
			t.Errorf("%s %q matched %q", tc.typ, tc.val, tc.other)
		}
		// The same value under another type is a different criterion.
		if task.TargetsAny([]TargetIdentifier{{Type: TargetSUPI, Value: tc.val}}) {
			t.Errorf("%s %q matched a SUPI with the same value", tc.typ, tc.val)
		}
	}
}

// TestNamesAnyTypeAsksAboutKindsNotValues: the question is whether a warrant is of a
// shape the element could ever match, so only the identifier types count.
func TestNamesAnyTypeAsksAboutKindsNotValues(t *testing.T) {
	task := InterceptTask{Targets: []TargetIdentifier{
		{Type: TargetSUPI, Value: "262019876543210"},
		{Type: TargetFSEID, Value: "42"},
	}}

	for _, tc := range []struct {
		kinds []TargetIdentifierType
		want  bool
	}{
		{[]TargetIdentifierType{TargetSUPI}, true},
		{[]TargetIdentifierType{TargetPEI, TargetFSEID}, true},
		{[]TargetIdentifierType{TargetPEI, TargetGPSI}, false},
		{nil, false},
	} {
		if got := task.NamesAnyType(tc.kinds...); got != tc.want {
			t.Errorf("NamesAnyType(%v) = %v, want %v", tc.kinds, got, tc.want)
		}
	}

	if (InterceptTask{}).NamesAnyType(TargetSUPI) {
		t.Error("a task naming nothing names a SUPI")
	}
}

// TestSplitTargetsReturnsEveryMatchAndOnlyTheSubjectsOwnRest: every held identity the
// task names is a match, the rest of the subject's identities are "other", and an
// identity appearing twice is reported where it falls each time.
func TestSplitTargetsReturnsEveryMatchAndOnlyTheSubjectsOwnRest(t *testing.T) {
	supi := TargetIdentifier{Type: TargetSUPI, Value: "262019876543210"}
	pei := TargetIdentifier{Type: TargetPEI, Value: "35784005123456"}
	gpsi := TargetIdentifier{Type: TargetGPSI, Value: "4915123456789"}
	task := InterceptTask{Targets: []TargetIdentifier{supi, pei}}

	matched, other := task.SplitTargets([]TargetIdentifier{supi, gpsi, pei})
	if !slices.Equal(matched, []TargetIdentifier{supi, pei}) || !slices.Equal(other, []TargetIdentifier{gpsi}) {
		t.Errorf("SplitTargets = %v, %v; want both named identities matched and the GPSI other", matched, other)
	}

	matched, other = task.SplitTargets([]TargetIdentifier{gpsi})
	if matched != nil || !slices.Equal(other, []TargetIdentifier{gpsi}) {
		t.Errorf("SplitTargets with no match = %v, %v", matched, other)
	}

	if matched, other = task.SplitTargets(nil); matched != nil || other != nil {
		t.Errorf("SplitTargets(nil) = %v, %v; want nothing", matched, other)
	}

	if matched, _ = task.SplitTargets([]TargetIdentifier{supi, supi}); len(matched) != 2 {
		t.Errorf("a repeated held identity was matched %d times, want each occurrence", len(matched))
	}
}
