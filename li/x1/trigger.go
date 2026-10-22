// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/omec-project/util/li/types"
)

// Requester is the requesting side of X1 — the role TS 103 221-1 calls the
// "ADMF". A Triggering Function uses it to task a triggered POI: TS 33.128
// clause 5.2.6 realises the LI_T3 triggering interface as TS 103 221-1 with "the
// CC-TF playing the role of the ADMF … and the triggered CC-POI playing the role
// of the NE", so triggering is an ordinary X1 exchange rather than a private
// protocol. Requests carry the requester's own identifier, which the NE
// authenticates against the certificate presented (clause 8.2.4) exactly as it
// would a real ADMF's — being the SMF is not authority in itself.
//
// It logs nothing: what is tasked, and that anything is tasked at all, must not
// reach general operator logs.
type Requester struct {
	// authenticated records that this element was given TLS material, and therefore
	// that its peers' certificates are something it can hold them to.
	authenticated bool
	neURL         string // the NE's X1 endpoint
	ourID         string // identifier this requester asserts (the TF's own)
	neID          string // the NE being addressed
	client        *http.Client
	now           func() time.Time
}

// x1RequestTimeout bounds one outbound X1 exchange end to end — connection, request and the
// whole answer. It is what bounds every caller of postX1, which takes no context.
const x1RequestTimeout = 10 * time.Second

// maxResponseBytes bounds the answer this element reads to one of its own requests.
//
// Larger than maxRequestBytes, the server's limit on a request, because an answer's size is
// set by the state the peer holds rather than by what was asked: a GetAllDetailsResponse lists
// every task, at roughly 850 bytes for a minimal one, so the request limit would refuse the
// reconciliation answer of an element holding about 1,200 tasks — permanently, on every
// attempt. 16 MiB covers about twenty thousand while still bounding what a peer can make this
// element buffer.
const maxResponseBytes = 16 << 20

// maxDrainBytes is how much of an unread answer is discarded before its body is closed, so
// the connection can carry the next request. Small, because an answer this element refuses is
// not one it owes any further reading.
const maxDrainBytes = 64 << 10

// newX1Client is the HTTP client this element's outbound X1 requests go out on.
//
// It does not follow redirects. net/http follows up to ten by default, and a 307 or 308
// re-sends the POST body — a trigger's SEID and correlation, a fault report — to wherever
// the answer points, and only the last hop's certificate is ever checked against the peer
// this element meant to reach, after the body has already gone. A redirect is instead
// handed back as the answer, and readBoundResponse refuses any answer but 200, so it is a
// failed request.
func newX1Client(tlsConfig *tls.Config) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:   x1RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewRequester returns a Requester that POSTs to a triggered POI's X1 endpoint
// over mutual TLS, asserting ourID and addressing neID.
func NewRequester(neURL, ourID, neID string, tlsConfig *tls.Config) *Requester {
	return &Requester{
		neURL: neURL,
		ourID: ourID,
		neID:  neID,
		// Whether this element has credentials at all, which is what decides whether an
		// answer must prove whose it is. See bindsResponder.
		authenticated: tlsConfig != nil,
		client:        newX1Client(tlsConfig),
		now:           monotonicNow,
	}
}

// Trigger describes an LI_T3 task a Triggering Function installs at a triggered
// CC-POI (TS 33.128 table 6.2.3-6). XID, ProductID, CorrelationID, SEID and at least one
// DID are required, and ActivateTask and ModifyTask refuse a trigger without them: without
// ProductID and CorrelationID the POI's product cannot be attributed to a warrant or joined
// to the signalling, and without the SEID it cannot isolate the session at all.
// SEIDAddress is optional, as the FSEID's address is in the 3GPP X1 extension schema, and
// is omitted from the request when empty.
type Trigger struct {
	// XID identifies the trigger task itself and is allocated by the Triggering
	// Function. It is not the warrant's XID — that travels in ProductID.
	XID types.XID
	// ProductID is the warrant XID the POI must stamp on the PDUs it delivers.
	ProductID types.XID
	// CorrelationID is the value the POI must place in the correlation field of
	// those PDUs; it must equal the one the triggering POI used for the same
	// session's IRI.
	CorrelationID uint64
	// SEID and SEIDAddress are the packet detection criteria: the PFCP session
	// whose traffic is to be intercepted, and the address of the node that
	// allocated the SEID.
	SEID        uint64
	SEIDAddress string
	// DIDs reference destinations previously installed with CreateDestination.
	DIDs []string
}

// Destination is a delivery endpoint installed at an NE with CreateDestination
// (TS 103 221-1 clause 6.3.1). A trigger references it by DID rather than
// carrying an address, so a POI never needs a statically configured MDF.
type Destination struct {
	DID          string // UUIDv4
	FriendlyName string // optional
	DeliveryType string // X2Only | X3Only | X2andX3
	Address      string // IPv4 or IPv6 literal
	Port         uint16
}

// RequestError is an X1 error response. Callers can act on the code — e.g.
// TS 103 221-1 reserves 1030/1040 for an identity mismatch and 1080 for an
// unsupported request — rather than only logging text.
type RequestError struct {
	Code        int
	Description string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("x1: request refused with code %d: %s", e.Code, e.Description)
}

// ResponseError is an answer that could not be bound to the request that produced
// it. It is deliberately a different type from RequestError, because the two say
// different things and call for different responses.
//
// A *RequestError means the peer received the request, understood it, and refused
// it with a reason. That is a task-level condition: the triggering function knows
// which warrant it concerns and can report it as such.
//
// A *ResponseError means the answer cannot be attributed at all — which task it
// concerned is exactly what has not been established. That is an element-level
// condition, and the operator action differs: a refusal is something to take up
// with the point of interception, an unattributable answer is a configuration or
// routing fault on this side.
type ResponseError struct {
	// Field names what did not match — "neIdentifier", "x1TransactionId", the
	// response type. It is the operator's first clue and is safe to log on the LI
	// fault channel: it names a header field, never a target or a warrant.
	Field string
	// Want and Got carry the two values. Both are X1 identifiers or message types,
	// never interception detail.
	Want, Got string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("x1: response could not be attributed to the request that produced it: %s = %q, want %q",
		e.Field, e.Got, e.Want)
}

// responseTypeFor is the response a request type calls for. The naming is
// systematic in the schema — every FooRequest is answered by a FooResponse — so
// this is a suffix swap rather than a table, and a table would be one more thing
// to forget to extend.
func responseTypeFor(requestType string) string {
	return strings.TrimSuffix(requestType, "Request") + "Response"
}

// validateBound binds a decoded response to the request that produced it.
//
// It is shared by both senders on this interface, which sit on opposite sides of it: a
// Requester is an ADMF or a triggering function asking an NE, and a Reporter is an NE telling
// an ADMF. Both must bind their answers to their requests, and one implementation keeps the
// two from checking different things.
//
// h names the identifiers as the answer must carry them: OurID in admfIdentifier and NeID in
// neIdentifier. Which of the two is the peer's own assertion depends on the direction. An NE
// answering a Requester states neIdentifier about itself and echoes admfIdentifier; an ADMF
// answering a Reporter states admfIdentifier about itself and echoes neIdentifier. The
// asserted one is what detects a misroute — the other, like the transaction identifier and
// the response type, is derived from the request and any endpoint that received it can return
// it correctly.
//
// So these checks catch a *wrong* peer and not a lying one, which can state whatever is
// expected. readBoundResponse adds the check that bounds the second case: the responder's
// certificate must bind the expected identifier in the peer's role (clause 8.2.4), as the
// server checks a requester's certificate inbound. A hostname binds a name; the annex G
// binding binds an X1 identifier, and inside one LI trust domain those are not the same
// statement.
//
// Every field checked here is a member of the schema's X1ResponseMessage base type, so every
// response type carries all of them and each check can be required. Deliberately absent is the
// acknowledgement: `oK` is *not* on the base type — a GetAllDetailsResponse carries none — so
// requiring one would refuse every details answer. That is the check callers apply for
// themselves where their own response type defines one.
func validateBound(h header, out X1Response) (X1ResponseMessage, error) {
	if len(out.Messages) == 0 {
		return X1ResponseMessage{}, &ResponseError{Field: "message count", Want: "1", Got: "0"}
	}
	if len(out.Messages) > 1 {
		// Every request this element sends asks one question, so a container carrying
		// several answers cannot be attributed to it — and clause 6.1 agrees: a
		// ResponseContainer holds "all the responses to the requests in the container".
		// Taking the first would be choosing which answer to believe.
		return X1ResponseMessage{}, &ResponseError{
			Field: "message count", Want: "1", Got: strconv.Itoa(len(out.Messages)),
		}
	}

	m := out.Messages[0]

	// An ErrorResponse is a legitimate answer to any request, so the type check
	// admits it and the caller turns it into a *RequestError below. What is refused
	// is an answer to a different question.
	if got := localType(m.Type); got != responseTypeFor(h.Type) && got != errorResponse {
		return X1ResponseMessage{}, &ResponseError{Field: "response type", Want: responseTypeFor(h.Type), Got: got}
	}
	if m.X1TransactionID != h.TxID {
		// Sound as an equality check only because this requester always generates a TS
		// 103 280 UUID: a conformant server that receives a non-conformant identifier
		// is *required* to answer with a different one. Pinned by
		// TestTheRequesterAlwaysGeneratesAConformantTransactionIdentifier, so that
		// property cannot quietly stop holding.
		return X1ResponseMessage{}, &ResponseError{Field: "x1TransactionId", Want: h.TxID, Got: m.X1TransactionID}
	}
	// One of these two is the peer's assertion about itself, and that one detects a
	// misroute; the other is an echo. Both are required either way — see the doc comment.
	if m.NeIdentifier != h.NeID {
		return X1ResponseMessage{}, &ResponseError{Field: "neIdentifier", Want: h.NeID, Got: m.NeIdentifier}
	}
	if m.AdmfIdentifier != h.OurID {
		return X1ResponseMessage{}, &ResponseError{Field: "admfIdentifier", Want: h.OurID, Got: m.AdmfIdentifier}
	}
	if !versionPattern.MatchString(m.Version) {
		// Table 6.1-1 defines Version as the version of the present document "used for
		// encoding the message", so a peer answers in its own version rather than echoing
		// this element's, and clause 4.5 keeps minor and patch increments backwards
		// compatible within a major version. What is refused is anything outside the
		// schema's v1.x.y restriction: another major version, or no version at all.
		return X1ResponseMessage{}, &ResponseError{Field: "version", Want: "v1.x.y", Got: m.Version}
	}

	return m, nil
}

// bindsResponderCert checks the TS 103 221-1 clause 8.2.4 binding on the answering side:
// the certificate the peer presented must carry identifier in the given role.
//
// It is the mirror of what the server applies to every inbound request. Without it, a
// misrouted, stale or compromised endpoint inside the LI domain could acknowledge tasking,
// and the triggering function would record an interception as installed at an element that
// never received it — with nothing downstream to reveal it, because the product that would
// be missing is never produced.
//
// Shared by both senders on this interface, because both have the same obligation and the
// only thing that differs is which role the peer holds — an NE answering a triggering
// function, or an ADMF answering an NE's fault report.
//
// authenticated says whether this element holds TLS material at all. A Requester or
// Reporter built without a TLS configuration speaks plain HTTP: there is no certificate to
// bind and no chain to have verified, so none is required. That mode is outbound only — an
// X1 server without TLS answers every request with 1030, because it cannot authenticate the
// requester.
func bindsResponderCert(resp *http.Response, authenticated bool, role, identifier string) error {
	if !authenticated {
		return nil
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		// Configured with credentials and answered over a connection that presented
		// none: the peer is not authenticated at all, and an acknowledgement this
		// element cannot attribute to a party is not one it can act on.
		return &ResponseError{Field: "peer certificate", Want: identifier, Got: "none"}
	}
	if !certBinds(resp.TLS.PeerCertificates[0], role, identifier) {
		return &ResponseError{
			Field: "peer certificate",
			Want:  identifier,
			Got:   certIdentifier(resp.TLS.PeerCertificates[0], role),
		}
	}

	return nil
}

// readResponse is the Requester's entry into readBoundResponse, with the peer in the NE
// role. Every request type reads its answer through it, so none can reach a response
// without the status, decode and binding checks.
func (r *Requester) readResponse(h header, resp *http.Response) (X1ResponseMessage, error) {
	return readBoundResponse(h, resp, r.authenticated, roleNE, h.NeID)
}

// readBoundResponse is every step between an HTTP response and a message this element is
// entitled to act on: status, decode, the responder's certificate binding, and the message
// binding.
//
// Shared by the two senders on this interface — a Requester asking an NE and a Reporter
// telling an ADMF — because both have the same obligation and only the peer's role differs.
// role and identifier are that peer's; authenticated says whether this element holds
// credentials at all.
func readBoundResponse(
	h header, resp *http.Response, authenticated bool, role, identifier string,
) (X1ResponseMessage, error) {
	if resp.StatusCode != http.StatusOK {
		// Clause 7.2.2.2: HTTP error codes indicate HTTP-level errors only, and an
		// X1-level error "shall be … returned as a HTTP 200 OK response". So a non-200
		// is a transport fault, not a refusal, and must not be read as one. What is left
		// of the body is discarded so the connection can be reused.
		//nolint:errcheck // a drain that fails only costs the connection's reuse
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))

		return X1ResponseMessage{}, fmt.Errorf("x1: peer returned status %d", resp.StatusCode)
	}

	// Buffered rather than streamed, because a TopLevelError is a different root element
	// and telling one from a malformed response means looking at the bytes again — and
	// bounded, so a peer inside the LI domain cannot hold this element's memory as well
	// as its connection. One byte past the limit is read so that an answer over it is
	// refused as too large rather than misreported as malformed XML.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return X1ResponseMessage{}, fmt.Errorf("x1: reading response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return X1ResponseMessage{}, fmt.Errorf("x1: response exceeds %d bytes", maxResponseBytes)
	}

	// The responder is who its certificate says it is, before anything it says about
	// itself is read. Placed here rather than in validateBound because that works on a
	// decoded message and this is a property of the connection that carried it — and
	// because putting it here means no request type can reach a response without it.
	//
	// A response over a connection with no peer certificate is refused for the same
	// reason: this interface is mutually authenticated, and an answer this element
	// cannot bind to an element is not an acknowledgement it can act on.
	if err := bindsResponderCert(resp, authenticated, role, identifier); err != nil {
		return X1ResponseMessage{}, err
	}

	var out X1Response
	if err := xml.Unmarshal(body, &out); err != nil {
		// A TopLevelErrorResponse is a different root element, so it lands here rather
		// than decoding into an empty X1Response. It means the peer could not parse
		// what we sent (clause 6.1) — an element-level condition, and one whose cause
		// is on this side.
		if isTopLevelError(body) {
			return X1ResponseMessage{}, &ResponseError{
				Field: "request", Want: "a parseable X1 request", Got: "TopLevelError",
			}
		}

		return X1ResponseMessage{}, fmt.Errorf("x1: malformed response: %w", err)
	}

	return validateBound(h, out)
}

// isTopLevelError reports whether a body is the clause 6.1 answer to a request the
// peer could not parse. Matched on the root element name rather than by decoding,
// since the point is that it is not an X1Response.
func isTopLevelError(body []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Local == "X1TopLevelErrorResponse"
		}
	}
}

// header is the set of fields common to every X1 request message.
type header struct {
	OurID, NeID, Timestamp, TxID, Type string
}

func (r *Requester) header(msgType string) header {
	return header{
		OurID:     r.ourID,
		NeID:      r.neID,
		Timestamp: x1Timestamp(r.now()),
		TxID:      newUUID(),
		Type:      msgType,
	}
}

// taskTemplate emits an ActivateTaskRequest or ModifyTaskRequest carrying LI_T3
// task details. Element order inside taskDetails follows the TS_103_221_01.xsd
// xs:sequence, which is normative: xId, targetIdentifiers, deliveryType,
// listOfDIDs, then correlationID and productID. The detection criteria ride in
// the 3GPP extension, whose elements are namespace-qualified because that schema
// sets elementFormDefault="qualified".
//
// deliveryType is fixed at X3Only: TS 33.128 table 6.2.3-6 requires it for this
// trigger, and a triggered CC-POI produces no IRI.
var taskTemplate = template.Must(template.New("x1task").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:ext="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6">
  <ns1:x1RequestMessage xsi:type="ns1:{{.Header.Type}}">
    <ns1:admfIdentifier>{{esc .Header.OurID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .Header.NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Header.Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .Header.TxID}}</ns1:x1TransactionId>
    <ns1:taskDetails>
      <ns1:xId>{{esc .XID}}</ns1:xId>
      <ns1:targetIdentifiers>
        <ns1:targetIdentifier>
          <ns1:targetIdentifierExtension>
            <ns1:Owner>3GPP</ns1:Owner>
            <ext:UPFLIT3TargetIdentifierExtensions>
              <ext:UPFLIT3TargetIdentifier>
                <ext:FSEID>
                  <ext:SEID>{{.SEID}}</ext:SEID>
{{- if .SEIDAddress}}
                  <ext:{{.SEIDAddressElement}}>{{esc .SEIDAddress}}</ext:{{.SEIDAddressElement}}>
{{- end}}
                </ext:FSEID>
              </ext:UPFLIT3TargetIdentifier>
            </ext:UPFLIT3TargetIdentifierExtensions>
          </ns1:targetIdentifierExtension>
        </ns1:targetIdentifier>
      </ns1:targetIdentifiers>
      <ns1:deliveryType>X3Only</ns1:deliveryType>
      <ns1:listOfDIDs>
{{- range .DIDs}}
        <ns1:dId>{{esc .}}</ns1:dId>
{{- end}}
      </ns1:listOfDIDs>
      <ns1:correlationID>{{.CorrelationID}}</ns1:correlationID>
      <ns1:productID>{{esc .ProductID}}</ns1:productID>
    </ns1:taskDetails>
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// deactivateTemplate emits a DeactivateTaskRequest, which carries only the XID.
var deactivateTemplate = template.Must(template.New("x1deact").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:DeactivateTaskRequest">
    <ns1:admfIdentifier>{{esc .Header.OurID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .Header.NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Header.Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .Header.TxID}}</ns1:x1TransactionId>
    <ns1:xId>{{esc .XID}}</ns1:xId>
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// destinationTemplate emits a CreateDestinationRequest. destinationDetails
// follows its xs:sequence (dId, friendlyName?, deliveryType, deliveryAddress),
// and the address itself is a TS 103 280 IPAddressPort, whose namespace is
// likewise elementFormDefault="qualified".
//
// `port` is a CHOICE of a TCPPort or a UDPPort *child element*, not a number in the
// element's text; a peer that parsed it as text would accept a request no validating
// peer does. X2 and X3 are carried over TCP (TS 103 221-2), so the arm is TCPPort.
var destinationTemplate = template.Must(template.New("x1dest").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:c="http://uri.etsi.org/03280/common/2017/07">
  <ns1:x1RequestMessage xsi:type="ns1:CreateDestinationRequest">
    <ns1:admfIdentifier>{{esc .Header.OurID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .Header.NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Header.Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .Header.TxID}}</ns1:x1TransactionId>
    <ns1:destinationDetails>
      <ns1:dId>{{esc .DID}}</ns1:dId>
{{- if .FriendlyName}}
      <ns1:friendlyName>{{esc .FriendlyName}}</ns1:friendlyName>
{{- end}}
      <ns1:deliveryType>{{esc .DeliveryType}}</ns1:deliveryType>
      <ns1:deliveryAddress>
        <ns1:ipAddressAndPort>
          <c:address>
            <c:{{.AddressElement}}>{{esc .Address}}</c:{{.AddressElement}}>
          </c:address>
          <c:port>
            <c:TCPPort>{{.Port}}</c:TCPPort>
          </c:port>
        </ns1:ipAddressAndPort>
      </ns1:deliveryAddress>
    </ns1:destinationDetails>
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// CreateDestination installs a delivery destination at the NE. It must succeed
// before a trigger referencing the DID is sent (TS 33.128 table 6.2.3-6:
// destinations "shall be configured … prior to first use").
func (r *Requester) CreateDestination(d Destination) error {
	if d.DID == "" || d.Address == "" || d.Port == 0 {
		return errors.New("x1: destination needs a DID, address and port")
	}
	if _, err := deliveryProducts(d.DeliveryType); err != nil {
		return fmt.Errorf("x1: destination: %w", err)
	}
	element, address := addressArm(d.Address)
	h := r.header("CreateDestinationRequest")

	return r.send(destinationTemplate, h, struct {
		Header         header
		DID            string
		FriendlyName   string
		DeliveryType   string
		Address        string
		AddressElement string
		Port           uint16
	}{
		Header:         h,
		DID:            d.DID,
		FriendlyName:   d.FriendlyName,
		DeliveryType:   d.DeliveryType,
		Address:        address,
		AddressElement: element,
		Port:           d.Port,
	})
}

// ActivateTask installs an LI_T3 trigger at the POI.
func (r *Requester) ActivateTask(t Trigger) error {
	return r.task("ActivateTaskRequest", t)
}

// ModifyTask updates an installed trigger — used when a session changes in a way
// that changes its detection criteria (TS 33.128 table 6.2.3-8).
func (r *Requester) ModifyTask(t Trigger) error {
	return r.task("ModifyTaskRequest", t)
}

func (r *Requester) task(msgType string, t Trigger) error {
	switch {
	case t.XID == "":
		return errors.New("x1: trigger needs an XID")
	case t.ProductID == "":
		// Without it the POI would label product with the trigger's XID, which
		// no MDF can attribute to the warrant.
		return errors.New("x1: trigger needs a ProductID (the warrant XID)")
	case t.CorrelationID == 0:
		// Zero is indistinguishable from "unset" and leaves the content
		// unjoinable to the session's IRI.
		return errors.New("x1: trigger needs a non-zero CorrelationID")
	case t.SEID == 0:
		return errors.New("x1: trigger needs a SEID to detect the session")
	case len(t.DIDs) == 0:
		return errors.New("x1: trigger needs at least one destination")
	}
	h := r.header(msgType)
	seidAddressElement, seidAddress := addressArm(t.SEIDAddress)

	return r.send(taskTemplate, h, struct {
		Header             header
		XID                string
		ProductID          string
		CorrelationID      string
		SEID               uint64
		SEIDAddress        string
		SEIDAddressElement string
		DIDs               []string
	}{
		Header:        h,
		XID:           string(t.XID),
		ProductID:     string(t.ProductID),
		CorrelationID: strconv.FormatUint(t.CorrelationID, 10),
		SEID:          t.SEID,
		// The FSEID's address is a choice of two elements, exactly as a
		// destination's is (see CreateDestination), and its IPv6 arm demands the
		// expanded eight-group form. addressArm answers both questions for both
		// paths, so they cannot disagree.
		SEIDAddress:        seidAddress,
		SEIDAddressElement: seidAddressElement,
		DIDs:               t.DIDs,
	})
}

// keepaliveTemplate emits a KeepaliveRequest. It carries only the common header:
// its purpose is to prove the requester is still there.
var keepaliveTemplate = template.Must(template.New("x1ka").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:KeepaliveRequest">
    <ns1:admfIdentifier>{{esc .Header.OurID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .Header.NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Header.Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .Header.TxID}}</ns1:x1TransactionId>
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// detailsTemplate emits a GetAllDetailsRequest, which carries only the header.
var detailsTemplate = template.Must(template.New("x1all").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:GetAllDetailsRequest">
    <ns1:admfIdentifier>{{esc .Header.OurID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .Header.NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Header.Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .Header.TxID}}</ns1:x1TransactionId>
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// TaskXIDs asks the NE what tasking it currently holds.
//
// A requester needs this after it has itself restarted: it comes back with no
// record of what it installed, while the NE still holds all of it, and tasking
// nobody can withdraw is exactly what must not exist. A liveness signal cannot
// substitute — a restarted requester is perfectly alive.
//
// A task reported with no xId is left out: DeactivateTask could not name it, and xId is
// mandatory in the schema's TaskDetails, so a conformant NE never reports one.
func (r *Requester) TaskXIDs() ([]types.XID, error) {
	reported, err := r.ReportedTasks()
	if err != nil {
		return nil, err
	}
	xids := make([]types.XID, 0, len(reported))
	for _, t := range reported {
		if t.TaskDetails.XID != "" {
			xids = append(xids, types.XID(t.TaskDetails.XID))
		}
	}

	return xids, nil
}

// ReportedTasks asks the NE what tasking it holds and returns each task with the
// status the NE reports for it.
//
// TaskXIDs answers "which tasks", which is what reconciliation after a restart
// needs. This answers "and how are they", which is what a triggering function
// needs in order to notice that a trigger it installed is not actually running —
// a task whose provisioning failed, or which carries an unresolved fault, is
// content interception that is not happening while everyone believes it is.
func (r *Requester) ReportedTasks() ([]TaskResponseDetails, error) {
	h := r.header("GetAllDetailsRequest")

	var body bytes.Buffer
	if err := detailsTemplate.Execute(&body, struct{ Header header }{Header: h}); err != nil {
		return nil, fmt.Errorf("x1: rendering %s: %w", h.Type, err)
	}

	resp, err := postX1(r.client, r.neURL, &body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	m, err := r.readResponse(h, resp)
	if err != nil {
		return nil, err
	}
	if m.ErrorInformation != nil {
		return nil, &RequestError{Code: m.ErrorInformation.ErrorCode, Description: m.ErrorInformation.ErrorDescription}
	}
	// No acknowledgement is checked for, and that is not an omission: `oK` is not a
	// member of the schema's X1ResponseMessage base type, and GetAllDetailsResponse
	// does not extend it with one. Requiring it here would refuse every details
	// answer this element ever receives.

	return m.ReportedTasks(), nil
}

// Keepalive tells the NE this requester is still present.
//
// It is what makes the other side's fail-safe safe to enable: a POI that purges
// tasking when its requester goes quiet would otherwise purge whenever no new task
// happened to arrive, and a requester that never announces itself cannot be
// distinguished from one that has died. Tasking that outlives the party
// responsible for it is the failure this pair prevents.
func (r *Requester) Keepalive() error {
	h := r.header("KeepaliveRequest")

	return r.send(keepaliveTemplate, h, struct{ Header header }{Header: h})
}

// DeactivateTask removes a trigger, ending interception at the POI. A Triggering
// Function sends it when the session ends (TS 33.128 clause 6.2.3.3.1) and when
// the warrant it derives from is withdrawn.
func (r *Requester) DeactivateTask(xid types.XID) error {
	if xid == "" {
		return errors.New("x1: deactivate needs an XID")
	}
	h := r.header("DeactivateTaskRequest")

	return r.send(deactivateTemplate, h, struct {
		Header header
		XID    string
	}{Header: h, XID: string(xid)})
}

// send renders a request, POSTs it, and interprets the response. A response
// carrying errorInformation is returned as a *RequestError; anything the NE
// answers other than an acknowledgement is an error, so a caller cannot mistake
// a refusal for a successful tasking.
func (r *Requester) send(tmpl *template.Template, h header, data any) error {
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		return fmt.Errorf("x1: rendering %s: %w", h.Type, err)
	}
	resp, err := postX1(r.client, r.neURL, &body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	m, err := r.readResponse(h, resp)
	if err != nil {
		return err
	}
	if m.ErrorInformation != nil {
		return &RequestError{Code: m.ErrorInformation.ErrorCode, Description: m.ErrorInformation.ErrorDescription}
	}
	if m.OK == "" {
		return fmt.Errorf("x1: response %q carried neither acknowledgement nor error", localType(m.Type))
	}

	return nil
}

// postX1 sends an X1 request body to url on client and returns the response, which the
// caller must close. Both senders on this interface go out through it.
//
// It takes no context, deliberately: every exchange is bounded by the client's
// x1RequestTimeout, and none of this element's callers holds a context that should cut a
// report or a trigger short — a fault report abandoned because the code path that noticed the
// fault moved on would be a report lost for no reason.
func postX1(client *http.Client, url string, body *bytes.Buffer) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("x1: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")

	return client.Do(req)
}

// escapeXML escapes text for inclusion in an XML element body.
func escapeXML(s string) string {
	var b bytes.Buffer
	//nolint:errcheck // writing to a bytes.Buffer cannot fail
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// addressArm names the element an address is rendered into and renders it in the
// form that element's type accepts. Both halves, from one place, because both are
// properties of the schema rather than of any caller.
//
// The X1 schemas carry an address as a choice of an IPv4 and an IPv6 element in
// several places — a destination's deliveryAddress, an FSEID's or an FTEID's node
// address — and every one of them is rendered here, so they cannot answer "which arm"
// differently. An IPv4-mapped IPv6 address (::ffff:a.b.c.d) names an IPv4 endpoint and
// is rendered in the IPv4 arm.
//
// The rendering half is the part that is easy to miss, and it is the same shape as
// the six-digit timestamp rule. TS 103 280 types an IPv6Address as
//
//	([0-9a-f]{4}:){7}([0-9a-f]{4})
//
// — eight groups of exactly four lowercase hexadecimal digits. Not "an IPv6
// address": no "::" compression and no dropped leading zeros. Go's net.IP.String
// produces both, so an address that arrived through any Go networking API is in
// precisely the form the schema refuses, and picking the right element for it
// still yields a message a validating peer discards.
//
// An address that does not parse is passed through unchanged, on the arm a colon
// picks — IPv4 literals contain none and IPv6 literals always do. There is nothing to
// normalise and nothing to be gained by guessing further: the schema refuses it
// either way, which is the honest outcome.
func addressArm(addr string) (element, value string) {
	ip := net.ParseIP(addr)
	switch {
	case ip == nil && strings.Contains(addr, ":"):
		return "IPv6Address", addr
	case ip == nil:
		return "IPv4Address", addr
	case ip.To4() != nil:
		return "IPv4Address", ip.To4().String()
	default:
		return "IPv6Address", expandIPv6(ip.To16())
	}
}

// expandIPv6 renders 16 address octets as the schema's eight four-digit groups.
func expandIPv6(b []byte) string {
	var out strings.Builder
	out.Grow(39) // 8 groups of 4 digits plus 7 separators

	for i := 0; i+1 < len(b); i += 2 {
		if i > 0 {
			out.WriteByte(':')
		}
		fmt.Fprintf(&out, "%02x%02x", b[i], b[i+1])
	}

	return out.String()
}
