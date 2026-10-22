// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// TestTokenWhitespaceIsNotPartOfAnIdentifier: the identifiers are xs:token types, whose
// surrounding whitespace the schema collapses away. A conformant ADMF may send them padded
// or across lines, and that is the same identifier: the task is acknowledged, keyed by the
// unpadded XID, and answered with the unpadded transaction identifier.
func TestTokenWhitespaceIsNotPartOfAnIdentifier(t *testing.T) {
	body := strings.NewReplacer(
		"<ns1:admfIdentifier>admfID<", "<ns1:admfIdentifier>\n      admfID  <",
		"<ns1:x1TransactionId>3741800e-971b-4aa9-85f4-466d2b1adc7f<",
		"<ns1:x1TransactionId> 3741800e-971b-4aa9-85f4-466d2b1adc7f\t<",
		"<ns1:xId>"+string(testXID)+"<", "<ns1:xId>\n        "+string(testXID)+"\n      <",
		"<ns1:dId>"+testDIDInActivateXML+"<", "<ns1:dId> "+testDIDInActivateXML+" <",
	).Replace(activateXML)

	st := store.New()
	m := serve(t, testServer(st), body)
	if m.OK == "" {
		t.Fatalf("a task with padded identifiers was refused: %+v", m.ErrorInformation)
	}
	if m.X1TransactionID != "3741800e-971b-4aa9-85f4-466d2b1adc7f" {
		t.Errorf("answered with x1TransactionId %q, want the identifier without its padding", m.X1TransactionID)
	}
	if _, held := st.Get(testXID); !held {
		t.Error("the task is not held under its XID: the padding became part of the key")
	}
}

// TestACorrelationIDIsReadInTheSchemasLexicalSpace: correlationID is an
// xs:nonNegativeInteger, whose lexical space admits surrounding whitespace and a leading "+".
func TestACorrelationIDIsReadInTheSchemasLexicalSpace(t *testing.T) {
	for _, spelled := range []string{"+5", " 5 ", "\n005\n"} {
		body := strings.Replace(activateXML, "<ns1:targetIdentifiers>",
			"<ns1:correlationID>"+spelled+"</ns1:correlationID>\n    <ns1:targetIdentifiers>", 1)

		st := store.New()
		mustAck(t, testServer(st, HonoursCorrelationID()), body)

		task, held := st.Get(testXID)
		if !held {
			t.Fatalf("%q: task not stored", spelled)
		}
		if task.CorrelationID != 5 {
			t.Errorf("correlationID %q stored as %d, want 5", spelled, task.CorrelationID)
		}
	}
}
