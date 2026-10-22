// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

// Package x2x3 encodes and decodes the ETSI TS 103 221-2 X2/X3 PDU used to
// deliver xIRI (X2) and xCC (X3) from a Point of Interception to a Mediation
// and Delivery Function. The wire layout (all multi-byte fields big-endian) is:
//
//	off 0      major version (1, = 0)
//	off 1      minor version (1, = 5)
//	off 2-3    PDU type (2)
//	off 4-7    header length (4, = 40 + conditional attributes)
//	off 8-11   payload length (4)
//	off 12-13  payload format (2)
//	off 14-15  payload direction (2)
//	off 16-31  XID (16, a UUID)
//	off 32-39  correlation ID (8)
//	off 40..   conditional attribute fields: [type(2)|length(2)|value]*
//	then       payload
//
// The layout and validation rules were read against ETSI TS 103 221-2 V1.10.1
// (2026-03) field by field; CONFORMANCE.md in this directory records the
// disposition of every header field, PDU type, conditional attribute and payload
// format the specification defines, including what this package does not
// implement. Read it before assuming a field is handled.
//
// Interoperability testing against an independent implementation exercises the
// parts both sides emit; the gaps CONFORMANCE.md lists are in fields neither
// implementation sends.
package x2x3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// The Version field (TS 103 221-2 clause 5.2.1) asserts which revision of the
// specification a PDU was created against, so it is a conformance claim and not
// a build stamp. Value 5 is what V1.6.1 through V1.9.1 define; V1.10.1 raised it
// to 6.
//
// It stays at 5 because of the peer, not this package: the reference
// implementation of the mediation side that this package interoperates with
// **refuses** a 0.6 PDU — it closes the connection and stores nothing. Clause
// 5.2.1 defines a minor increment as backwards-compatible, so a conformant peer
// has to keep decoding; this one does not, and raising the field would stop all
// delivery to it rather than degrade it. What would unblock it is a mediation
// function that accepts 0.6, not work in this package.
const (
	MajorVersion          = 0
	MinorVersion          = 5
	MandatoryHeaderLength = 40
	CorrelationIDLength   = 8
	xidLength             = 16

	// sizePrefixLength is how much of a PDU states its full size: version (2), PDU type
	// (2), header length (4) and payload length (4).
	sizePrefixLength = 12
	// maxTLVValueLength is the largest value a conditional attribute's 16-bit length
	// field can state.
	maxTLVValueLength = math.MaxUint16
)

// ErrIncomplete is returned by Unmarshal when the buffer does not yet hold a
// full PDU (header length + payload length). A stream reader should wait for
// more bytes and retry.
var ErrIncomplete = errors.New("x2x3: incomplete PDU")

// PDUType identifies the PDU kind (TS 103 221-2).
type PDUType uint16

const (
	PDUTypeX2           PDUType = 1
	PDUTypeX3           PDUType = 2
	PDUTypeKeepalive    PDUType = 3
	PDUTypeKeepaliveAck PDUType = 4
)

// PayloadFormat identifies the encoding of the payload.
type PayloadFormat uint16

const (
	PayloadFormatReserved     PayloadFormat = 0
	PayloadFormatETSI102232_1 PayloadFormat = 1
	PayloadFormat3GPP33128    PayloadFormat = 2 // xIRI / xCC defined by TS 33.128
	PayloadFormat3GPP33108    PayloadFormat = 3
	PayloadFormatProprietary  PayloadFormat = 4
	PayloadFormatIPv4         PayloadFormat = 5 // decapsulated inner IPv4 (X3 CC)
	PayloadFormatIPv6         PayloadFormat = 6 // decapsulated inner IPv6 (X3 CC)
	PayloadFormatEthernet     PayloadFormat = 7
	PayloadFormatRTP          PayloadFormat = 8
	PayloadFormatSIP          PayloadFormat = 9
	PayloadFormatDHCP         PayloadFormat = 10
	PayloadFormatRADIUS       PayloadFormat = 11
	PayloadFormatGTPU         PayloadFormat = 12
	PayloadFormatMSRP         PayloadFormat = 13
	PayloadFormat33108EPSIRI  PayloadFormat = 14
	PayloadFormatMIME         PayloadFormat = 15
	PayloadFormatUnstructured PayloadFormat = 16
	PayloadFormatPSPDUPayload PayloadFormat = 17 // ETSI TS 102 232-1 PS-PDU.Payload, added in V1.8.1
)

// payloadFormatRules records whether each format is permitted on X2 and X3
// respectively, transcribed from TS 103 221-2 table 5.4.1-1.
//
// TRANSCRIBED FROM V1.10.1 (2026-03). Restate the revision whenever this table
// is updated: the table is not checked against any published source, so without
// a version a stale copy and a current one look identical. That is exactly how
// value 17 stayed missing for three revisions. CONFORMANCE.md carries the
// per-value disposition, and x2x3_test.go asserts this whole table.
var payloadFormatRules = map[PayloadFormat][2]bool{
	PayloadFormatReserved:     {false, false},
	PayloadFormatETSI102232_1: {true, true},
	PayloadFormat3GPP33128:    {true, true},
	PayloadFormat3GPP33108:    {true, true},
	PayloadFormatProprietary:  {true, true},
	PayloadFormatIPv4:         {true, true},
	PayloadFormatIPv6:         {true, true},
	PayloadFormatEthernet:     {false, true},
	PayloadFormatRTP:          {false, true},
	PayloadFormatSIP:          {true, false},
	PayloadFormatDHCP:         {true, false},
	PayloadFormatRADIUS:       {true, false},
	PayloadFormatGTPU:         {false, true},
	PayloadFormatMSRP:         {false, true},
	PayloadFormat33108EPSIRI:  {true, false},
	PayloadFormatMIME:         {true, true},
	PayloadFormatUnstructured: {false, true},
	PayloadFormatPSPDUPayload: {true, true},
}

func (f PayloadFormat) allowedOn(t PDUType) bool {
	r, ok := payloadFormatRules[f]
	if !ok {
		return false
	}
	switch t {
	case PDUTypeX2:
		return r[0]
	case PDUTypeX3:
		return r[1]
	default:
		return true // keepalive PDUs do not carry a checked payload
	}
}

// PayloadDirection indicates the direction of the intercepted communication.
type PayloadDirection uint16

const (
	DirectionReservedKeepalive PayloadDirection = 0
	DirectionUnknown           PayloadDirection = 1
	DirectionToTarget          PayloadDirection = 2
	DirectionFromTarget        PayloadDirection = 3
	DirectionMultiple          PayloadDirection = 4
	DirectionNotApplicable     PayloadDirection = 5
)

// TLV is a conditional attribute field carried in the header (type, value); the
// 2-byte length is computed on the wire.
type TLV struct {
	Type  uint16
	Value []byte
}

// PDU is a decoded X2/X3 PDU. Header length and payload length are derived on
// encode and validated on decode, so they are not stored.
type PDU struct {
	Type          PDUType
	PayloadFormat PayloadFormat
	Direction     PayloadDirection
	XID           [xidLength]byte
	CorrelationID [CorrelationIDLength]byte
	Attributes    []TLV
	Payload       []byte
}

func (p *PDU) validate() error {
	if (p.Type == PDUTypeX2 || p.Type == PDUTypeX3) && !p.PayloadFormat.allowedOn(p.Type) {
		return fmt.Errorf("x2x3: payload format %d not allowed on PDU type %d", p.PayloadFormat, p.Type)
	}
	return nil
}

// Marshal encodes the PDU to its TS 103 221-2 wire form.
//
// **A nil receiver is refused rather than dereferenced**, and this is where it has to be:
// every delivery path in this package reaches the wire through here, so a check on one
// entry point leaves the others faulting. What faults is the network function carrying the
// delivery — an AMF's signalling goroutine or a UPF's framing worker — over invalid input,
// which takes the element down rather than the mediation function it was delivering to.
// Send, SendBatch and AsyncSender.Send refuse a nil unit at their own entry too, so the
// error a caller gets does not depend on which of them it used.
func (p *PDU) Marshal() ([]byte, error) {
	if p == nil {
		return nil, ErrNilPDU
	}

	return p.appendTo(nil)
}

// appendTo appends p's wire form to dst and returns the extended slice. On error dst is
// returned unchanged: every check runs before anything is written.
//
// It is what lets a batch be built in one buffer — Client.SendBatch appends each PDU straight
// into the write it will make — instead of marshalling each PDU and copying it in again.
func (p *PDU) appendTo(dst []byte) ([]byte, error) {
	if err := p.validate(); err != nil {
		return dst, err
	}

	// Size the attribute region before writing, so the header is written once into a
	// buffer of its final length. Building the TLVs into a separate slice first would
	// cost an allocation per PDU, which on X3 is an allocation per duplicated packet.
	//
	// Summed in int64, so the check below sees the true length even where int is 32 bits.
	var attrsLen int64
	for _, t := range p.Attributes {
		if len(t.Value) > maxTLVValueLength {
			return dst, fmt.Errorf("x2x3: TLV type %d value too long (%d bytes)", t.Type, len(t.Value))
		}
		attrsLen += 4 + int64(len(t.Value))
	}

	if err := lengthsFit(MandatoryHeaderLength+attrsLen, int64(len(p.Payload))); err != nil {
		return dst, err
	}
	headerLen := MandatoryHeaderLength + int(attrsLen) // guarded by lengthsFit

	start := len(dst)
	dst = grow(dst, headerLen+len(p.Payload))[:start+headerLen]
	out := dst[start:]
	out[0] = MajorVersion
	out[1] = MinorVersion
	binary.BigEndian.PutUint16(out[2:4], uint16(p.Type))
	binary.BigEndian.PutUint32(out[4:8], uint32(headerLen))       // guarded by lengthsFit
	binary.BigEndian.PutUint32(out[8:12], uint32(len(p.Payload))) // guarded by lengthsFit
	binary.BigEndian.PutUint16(out[12:14], uint16(p.PayloadFormat))
	binary.BigEndian.PutUint16(out[14:16], uint16(p.Direction))
	copy(out[16:32], p.XID[:])
	copy(out[32:40], p.CorrelationID[:])

	off := MandatoryHeaderLength
	for _, t := range p.Attributes {
		binary.BigEndian.PutUint16(out[off:off+2], t.Type)
		binary.BigEndian.PutUint16(out[off+2:off+4], uint16(len(t.Value))) // checked above
		off += 4
		off += copy(out[off:], t.Value)
	}

	return append(dst, p.Payload...), nil
}

// grow returns b with room for n more bytes. A buffer with no room is replaced by one sized
// exactly for a lone PDU and doubled for a batch, so marshalling one PDU allocates once and a
// batch is not copied once per PDU.
func grow(b []byte, n int) []byte {
	if cap(b)-len(b) >= n {
		return b
	}

	c := max(2*cap(b), len(b)+n)
	nb := make([]byte, len(b), c)
	copy(nb, b)

	return nb
}

// lengthsFit refuses a header or payload too long for the 32-bit length field that states it.
// Writing the truncated length would produce a PDU whose own framing is wrong, which a
// receiver reading a stream of them cannot recover from — every PDU after it is misframed.
//
// It also refuses a PDU whose total length is not an int, which is reachable only where int
// is 32 bits: the buffer that would hold it cannot be allocated.
func lengthsFit(headerLen, payloadLen int64) error {
	if headerLen > math.MaxUint32 {
		return fmt.Errorf("x2x3: header of %d bytes does not fit its 32-bit length field", headerLen)
	}
	if payloadLen > math.MaxUint32 {
		return fmt.Errorf("x2x3: payload of %d bytes does not fit its 32-bit length field", payloadLen)
	}
	if headerLen+payloadLen > math.MaxInt {
		return fmt.Errorf("x2x3: a PDU of %d bytes is too large to build", headerLen+payloadLen)
	}

	return nil
}

// Unmarshal decodes a single PDU from the front of b, returning the PDU and the
// number of bytes it consumed (so callers can frame a stream). It returns
// ErrIncomplete if b does not yet hold a complete PDU.
func Unmarshal(b []byte) (*PDU, int, error) {
	if len(b) < sizePrefixLength {
		return nil, 0, ErrIncomplete
	}
	headerLen := binary.BigEndian.Uint32(b[4:8])
	payloadLen := binary.BigEndian.Uint32(b[8:12])
	if headerLen < MandatoryHeaderLength {
		return nil, 0, fmt.Errorf("x2x3: header length %d below mandatory %d", headerLen, MandatoryHeaderLength)
	}
	if uint64(headerLen)+uint64(payloadLen) > uint64(len(b)) {
		return nil, 0, ErrIncomplete
	}
	total := int(headerLen) + int(payloadLen) // safe: the uint64 check above bounds the sum by len(b) ≤ maxInt
	// TS 103 221-2 clause 5.2.1 defines the major version as incrementing on a
	// backwards-incompatible change and the minor on a backwards-compatible
	// addition, so a peer ahead of us on the minor number is required to remain
	// decodable: it can only have added a PDU type, direction, conditional
	// attribute or payload format, none of which changes the layout parsed here.
	// Rejecting that peer outright would refuse a conformant MDF running a newer
	// revision than ours.
	// A differing minor in either direction stays decodable: a lower one uses a
	// subset of what we know, a higher one adds values that do not move any field.
	if b[0] != MajorVersion {
		return nil, 0, fmt.Errorf("x2x3: unsupported major version %d (this implementation speaks %d.x)", b[0], MajorVersion)
	}

	p := &PDU{
		Type:          PDUType(binary.BigEndian.Uint16(b[2:4])),
		PayloadFormat: PayloadFormat(binary.BigEndian.Uint16(b[12:14])),
		Direction:     PayloadDirection(binary.BigEndian.Uint16(b[14:16])),
	}
	copy(p.XID[:], b[16:32])
	copy(p.CorrelationID[:], b[32:40])

	attrs, err := parseTLVs(b[MandatoryHeaderLength:headerLen])
	if err != nil {
		return nil, 0, err
	}
	p.Attributes = attrs
	p.Payload = append([]byte(nil), b[headerLen:total]...)

	if err := p.validate(); err != nil {
		return nil, 0, err
	}
	return p, total, nil
}

func parseTLVs(b []byte) ([]TLV, error) {
	var out []TLV
	for len(b) > 0 {
		if len(b) < 4 {
			return nil, errors.New("x2x3: truncated TLV header")
		}
		typ := binary.BigEndian.Uint16(b[0:2])
		n := int(binary.BigEndian.Uint16(b[2:4]))
		if len(b) < 4+n {
			return nil, errors.New("x2x3: truncated TLV value")
		}
		out = append(out, TLV{Type: typ, Value: append([]byte(nil), b[4:4+n]...)})
		b = b[4+n:]
	}
	return out, nil
}
