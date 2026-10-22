// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// inlineMembers names, for each constrained type the module does not define under its own name,
// the member whose inline restriction it carries. Every other type in the tables is defined in
// the module under the name this package gives it.
var inlineMembers = map[string]string{
	"SliceServiceType":       "sliceServiceType",
	"SliceDifferentiator":    "sliceDifferentiator",
	"ServiceType":            "serviceType",
	"TEID":                   "tEID",
	"LCSCorrelationID":       "lcsCorrelationId",
	"RoutingIndicatorLength": "routingIndicatorLength",
}

// moduleBounds reads a constrained type's bounds from the module: the `(lo..hi)` or `(SIZE(n))`
// on its own definition, or on the member it is declared inline in.
func moduleBounds(t *testing.T, src []byte, name string) (int64, int64) {
	t.Helper()

	line := regexp.MustCompile(`(?m)^` + name + ` ::= .*$`)
	if member, ok := inlineMembers[name]; ok {
		line = regexp.MustCompile(`(?m)^\s*` + member + `\s+\[\d+\][^\n]*$`)
	}
	def := line.Find(src)
	if def == nil {
		t.Fatalf("%s: no definition in %s", name, modulePath)
	}

	m := regexp.MustCompile(`\((?:SIZE\()?\s*(\d+)\s*(?:\.\.\s*(\d+))?\s*\)`).FindSubmatch(def)
	if m == nil {
		t.Fatalf("%s: %q carries no bounds", name, def)
	}
	lo, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	hi := lo
	if len(m[2]) > 0 {
		if hi, err = strconv.ParseInt(string(m[2]), 10, 64); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	return lo, hi
}

// TestEveryConstraintBoundIsEnforced holds every entry of the size and range tables to the
// module, and checks each is enforced at both of the module's bounds and one step outside each.
//
// The bounds come from the module rather than from the tables, so an entry widened or
// transcribed one off is caught by the comparison and by the value one step past the module's
// bound being accepted. Each value is checked as a mandatory member with zero forbidden, so the
// exemptions an optional member gets — empty or zero as absence — cannot mask a bound.
// (Enumerations are TestEnumBoundsMatchTheModule's and TestCauseBoundsMatchTheModule's.)
func TestEveryConstraintBoundIsEnforced(t *testing.T) {
	src, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatalf("reading the module: %v", err)
	}

	check := func(t *testing.T, v reflect.Value, wantOK bool, what string) {
		t.Helper()

		err := constraintOf(v, "x", true, true)
		switch {
		case wantOK && err != nil:
			t.Errorf("%s was refused: %v", what, err)
		case !wantOK && err == nil:
			t.Errorf("%s was accepted", what)
		}
	}
	agrees := func(t *testing.T, name string, tableLo, tableHi int64) (int64, int64) {
		t.Helper()

		lo, hi := moduleBounds(t, src, name)
		if tableLo != lo || tableHi != hi {
			t.Errorf("the table bounds %s to %d..%d, and the module defines %d..%d",
				name, tableLo, tableHi, lo, hi)
		}
		return lo, hi
	}

	for typ, c := range intConstraints {
		t.Run(typ.Name(), func(t *testing.T) {
			lo, hi := agrees(t, typ.Name(), c.min, c.max)
			at := func(n int64) reflect.Value {
				v := reflect.New(typ).Elem()
				v.SetInt(n)
				return v
			}
			check(t, at(lo-1), false, "one below the lower bound")
			check(t, at(lo), true, "the lower bound")
			check(t, at(hi), true, "the upper bound")
			check(t, at(hi+1), false, "one above the upper bound")
		})
	}

	for typ, c := range octetConstraints {
		t.Run(typ.Name(), func(t *testing.T) {
			lo, hi := agrees(t, typ.Name(), int64(c.min), int64(c.max))
			at := func(n int64) reflect.Value { return reflect.MakeSlice(typ, int(n), int(n)) }
			check(t, at(lo-1), false, "one octet short of the lower bound")
			check(t, at(lo), true, "the lower bound")
			check(t, at(hi), true, "the upper bound")
			check(t, at(hi+1), false, "one octet past the upper bound")
		})
	}

	strs := func(table map[reflect.Type]utf8Size, char string) {
		for typ, c := range table {
			t.Run(typ.Name(), func(t *testing.T) {
				lo, hi := agrees(t, typ.Name(), int64(c.min), int64(c.max))
				at := func(n int64) reflect.Value {
					v := reflect.New(typ).Elem()
					v.SetString(strings.Repeat(char, int(n)))
					return v
				}
				check(t, at(lo-1), false, "one character short of the lower bound")
				check(t, at(lo), true, "the lower bound")
				check(t, at(hi), true, "the upper bound")
				check(t, at(hi+1), false, "one character past the upper bound")
			})
		}
	}
	strs(numericConstraints, "1")
	strs(identityConstraints, "1")
	// A multi-byte character, so the bound is checked in characters, as the module's SIZE on a
	// UTF8String counts them, rather than in bytes.
	strs(utf8Constraints, "é")
}

// TestEnumBoundsMatchTheModule holds every enumConstraints entry outside the five cause groups
// to the module: the bounds are the lowest and highest value the module's ENUMERATED of the same
// name defines. (TestCauseBoundsMatchTheModule covers the cause groups.)
func TestEnumBoundsMatchTheModule(t *testing.T) {
	src, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatalf("reading the module: %v", err)
	}

	causeGroups := map[string]bool{
		"CauseRadioNetwork": true, "CauseTransport": true, "CauseNas": true,
		"CauseProtocol": true, "CauseMisc": true,
	}
	entry := regexp.MustCompile(`[A-Za-z][\w-]*\((\d+)\)`)

	checked := 0
	for typ, bounds := range enumConstraints {
		name := typ.Name()
		if causeGroups[name] {
			continue
		}

		block := regexp.MustCompile(`(?ms)^` + name + ` ::= ENUMERATED\s*\{(.*?)^\}`).FindSubmatch(src)
		if block == nil {
			t.Errorf("%s is not defined as an ENUMERATED in %s", name, modulePath)
			continue
		}

		lo, hi := int64(-1), int64(-1)
		for _, m := range entry.FindAllStringSubmatch(string(block[1]), -1) {
			v, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				t.Fatalf("%s: %q is not a number", name, m[1])
			}
			if lo < 0 || v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		if lo < 0 {
			t.Errorf("%s parsed to no values, so this comparison would pass against anything", name)
			continue
		}
		if bounds.min != lo || bounds.max != hi {
			t.Errorf("enumConstraints bounds %s to %d..%d, and the module defines %d..%d",
				name, bounds.min, bounds.max, lo, hi)
		}
		checked++
	}

	if checked < 10 {
		t.Errorf("checked %d enumerations against the module; the table holds eleven outside "+
			"the cause groups, so the parse is looking at something else", checked)
	}
}
