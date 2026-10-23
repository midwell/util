// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
)

// TestTheShapeCheckIsLinearInAnExtensionsContent: an extension placeholder's content is an xs:any
// of unbounded occurrence that the sender chooses freely, and it is decoded before the request is
// authenticated. A shape check that compared each repeated child name against every other one
// let a request of under 1 MiB -- tens of thousands of distinct elements, each sent twice -- hold
// a core for seconds. The cost of eight times the content must stay near eight times the cost.
//
// The sizes are large because the quadratic form only separates from the linear one there: at
// 1000 and 4000 pairs it cost under 10x for 4x the content, which a bound loose enough to absorb
// a loaded machine's noise could not tell from linear. At 5000 and 40000 pairs it costs about
// 40x for 8x, and the linear form about 7x.
func TestTheShapeCheckIsLinearInAnExtensionsContent(t *testing.T) {
	siblings := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "<a%d/><a%d/>", i, i)
		}

		return b.String()
	}

	for _, c := range []struct {
		name, open, close string
		into              func() any
	}{
		{
			"targetIdentifierExtension", `<targetIdentifierExtension><Owner>3GPP</Owner>`, `</targetIdentifierExtension>`,
			func() any { return new(TargetIdentifierExtension) },
		},
		{
			"taskDetailsExtensions", `<taskDetailsExtensions><Owner>3GPP</Owner>`, `</taskDetailsExtensions>`,
			func() any { return new(MessageExtension) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The best of a few runs, so a pause on a loaded machine does not count; fewer
			// for the larger body, which is most of the test's time.
			cost := func(n, runs int) time.Duration {
				body := []byte(c.open + siblings(n) + c.close)
				best := time.Duration(math.MaxInt64)
				for range runs {
					start := time.Now()
					if err := xml.Unmarshal(body, c.into()); err != nil {
						t.Fatalf("decode: %v", err)
					}
					best = min(best, time.Since(start))
				}

				return best
			}

			small, large := cost(5000, 3), cost(40000, 2)
			t.Logf("5000 pairs %s, 40000 pairs %s", small, large)
			if large > 20*small {
				t.Errorf("40000 repeated pairs took %s against %s for 5000: %.1fx for 8x the content",
					large, small, float64(large)/float64(small))
			}
		})
	}
}

// TestARefusalQuotesAFewNamesNotAll: a refusal names the unmodelled elements a request carried,
// so the peer can see its mistake -- but a request's worth of them made the answer as large as
// the request.
func TestARefusalQuotesAFewNamesNotAll(t *testing.T) {
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "<ns1:unmodelled%d>x</ns1:unmodelled%d>", i, i)
	}
	for _, c := range []struct{ name, body string }{
		{"target identifier extension", withTarget(`<ns1:targetIdentifierExtension><ns1:Owner>3GPP</ns1:Owner>` +
			b.String() + `</ns1:targetIdentifierExtension>`)},
		{"task extension", withTaskDetails("</ns1:taskDetails>", `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>`+
			b.String()+`</ns1:taskDetailsExtensions></ns1:taskDetails>`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := serve(t, testServer(store.New()), c.body)
			if m.ErrorInformation == nil {
				t.Fatal("the request was acknowledged")
			}
			d := m.ErrorInformation.ErrorDescription
			if !strings.Contains(d, "unmodelled0") || !strings.Contains(d, "992 more") {
				t.Errorf("description %q does not name the first elements and count the rest", d)
			}
			if len(d) > 512 {
				t.Errorf("description is %d bytes for one refusal", len(d))
			}
		})
	}
}
