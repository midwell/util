// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

var testGUTI = FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 1}

// TestAMandatoryMemberCannotBeSentEmpty: the emitter writes a mandatory member whatever it
// holds, so an empty one went out as a zero-length value its own type forbids. constraints.go
// used to say "the codec refuses that"; it did not, and these records all encoded.
func TestAMandatoryMemberCannotBeSentEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{"a zero GUTI where the GUTI is mandatory", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"),
		}, "GUTI.MCC is empty"},
		{"a nil UE policy", AMFUEPolicyTransfer{
			SUPI: IMSI("262019876543210"),
		}, "UEPolicy carries no octets"},
		{"an empty LCS correlation id", AMFPositioningInfoTransfer{
			SUPI: IMSI("262019876543210"),
		}, "LCSCorrelationID is empty"},
		{"a tracking area with no TAC", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI,
			FiveGSTAIList: TAIList{{PLMNID: PLMNID{MCC: "262", MNC: "01"}}},
		}, "TAC carries no octets"},
		{"a present but empty slice differentiator", SMFPDUSessionModification{
			SNSSAI:      SNSSAI{SliceServiceType: 1, SliceDifferentiator: SliceDifferentiator{}},
			RequestType: SMRequestModification,
		}, "SliceDifferentiator carries no octets"},
		{"a present but empty subscriber-identity list", AMFUEServiceAccept{
			UserIdentifiers:        UserIdentifiers{FiveGS: FiveGSSubscriberIDs{IDs: []any{}}},
			ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		}, "SEQUENCE SIZE(1..MAX)"},
		{"an empty endpoint address in a list", SMFPDUSessionEstablishment{
			PDUSessionID: 5, PDUSessionType: PDUSessionTypeIPv4, DNN: "internet",
			RequestType: SMRequestInitial, UEEndpoint: []any{IPv4Address(nil)},
		}, "carries no octets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("encoded a mandatory member as an empty value its type forbids")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not say what is wrong.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}
}

// TestIdentityLeavesAreCheckedAgainstTheirOwnTypes: a record's SUPI, PEI and GPSI come from the
// network function's own context, not from a validated task, so a value violating its type
// went out — a fifteen-digit IMEI where TS 33.128 defines fourteen, which is what every record
// for an IMEI-identified UE used to carry.
func TestIdentityLeavesAreCheckedAgainstTheirOwnTypes(t *testing.T) {
	release := func(supi, pei, gpsi any) SMFPDUSessionRelease {
		return SMFPDUSessionRelease{SUPI: supi, PEI: pei, GPSI: gpsi, PDUSessionID: 5}
	}

	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{"an empty mandatory SUPI", release(IMSI(""), nil, nil), "IMSI is defined as NumericString (SIZE(6..15))"},
		{"a five-digit IMSI", release(IMSI("26201"), nil, nil), "SIZE(6..15)"},
		{"a sixteen-digit IMSI", release(IMSI("2620198765432101"), nil, nil), "SIZE(6..15)"},
		{"an IMSI with its TS 29.571 prefix", release(IMSI("imsi-262019876543210"), nil, nil), "SIZE(6..15)"},
		{"an IMSI with a letter", release(IMSI("26201987654321a"), nil, nil), "is a string of digits"},
		{"an IMEI with its check digit", release(IMSI("262019876543210"), IMEI("356938035643809"), nil), "IMEI is defined as NumericString (SIZE(14..14))"},
		{"a fifteen-digit IMEISV", release(IMSI("262019876543210"), IMEISV("356938035643809"), nil), "SIZE(16..16)"},
		{"a sixteen-digit MSISDN", release(IMSI("262019876543210"), nil, MSISDN("4915123456789012")), "SIZE(1..15)"},
		{"an MSISDN in E.164 + form", release(IMSI("262019876543210"), nil, MSISDN("+4915123456789")), "is a string of digits"},
		{"a bad identity inside userIdentifiers", AMFUEServiceAccept{
			UserIdentifiers:        Identifiers(IMSI("262019876543210"), IMEI("356938035643809"), nil),
			ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		}, "IMEI is defined as"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("encoded an identity its own type forbids")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not name the leaf and its definition.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}

	// The conformant forms must pass, or the cases above prove nothing.
	if _, err := EncodeXIRI(release(IMSI("262019876543210"), IMEI("35693803564380"), MSISDN("4915123456789"))); err != nil {
		t.Errorf("conformant identities were refused: %v", err)
	}
	if _, err := EncodeXIRI(release(IMSI("262011"), IMEISV("3569380356438091"), MSISDN("1"))); err != nil {
		t.Errorf("conformant identities at the bounds were refused: %v", err)
	}
}

// TestEncodeXIRIRefusesNoEvent: a nil event panicked in the constraint walk.
func TestEncodeXIRIRefusesNoEvent(t *testing.T) {
	if _, err := EncodeXIRI(nil); err == nil {
		t.Error("a nil event was encoded")
	}
}

// TestRATTypeIsBoundedByTheModule: RATType had no range check, so any integer went out.
func TestRATTypeIsBoundedByTheModule(t *testing.T) {
	rec := func(r RATType) AMFRegistration {
		return AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI, RATType: r,
		}
	}

	for _, bad := range []RATType{34, 99, -1} {
		if _, err := EncodeXIRI(rec(bad)); err == nil {
			t.Errorf("RATType %d is not defined by the module and was encoded", bad)
		}
	}
	// 21 (wBEUTRANLEO) and 33 (nREREDCAP) are the module's own, though this package names
	// neither; 0 is absence.
	for _, ok := range []RATType{0, 1, 21, 33} {
		if _, err := EncodeXIRI(rec(ok)); err != nil {
			t.Errorf("RATType %d was refused: %v", ok, err)
		}
	}
}

// TestEveryEndpointListRefusesThePresentEmptyForm: the guard covered two of the records carrying
// a ueEndpoint list. SMFUnsuccessfulProcedure's encoded a present empty list, which asserts the
// failed session had no address.
func TestEveryEndpointListRefusesThePresentEmptyForm(t *testing.T) {
	_, err := EncodeXIRI(SMFUnsuccessfulProcedure{
		FailedProcedureType: SMFFailedPDUSessionEstablishment, FailureCause: 1,
		Initiator: InitiatorNetwork, UEEndpoint: []any{},
	})
	if err == nil || !strings.Contains(err.Error(), "present but empty uEEndpoint") {
		t.Errorf("a present empty endpoint list was not refused: %v", err)
	}
}

// TestTheModificationEndpointIsOneAddress: SMFPDUSessionModification's uEEndpoint is the CHOICE
// itself. As a []any it could carry two alternatives under one [13], or none, and both encode as
// structures a conformant decoder refuses.
func TestTheModificationEndpointIsOneAddress(t *testing.T) {
	rec := func(ep any) SMFPDUSessionModification {
		return SMFPDUSessionModification{RequestType: SMRequestModification, UEEndpoint: ep}
	}

	der, err := EncodeXIRI(rec(UEEndpointAddress(net.ParseIP("10.45.0.2"))))
	if err != nil {
		t.Fatal(err)
	}
	// [13] EXPLICIT { [1] IMPLICIT OCTET STRING 0a2d0002 }.
	if !strings.Contains(hex.EncodeToString(der), "ad0681040a2d0002") {
		t.Errorf("the endpoint is not one [13]-wrapped alternative: %x", der)
	}

	// Absent: the record is exactly the eight octets of [13] { [1] four octets } shorter.
	without, err := EncodeXIRI(rec(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(der)-len(without) != 8 {
		t.Errorf("an absent endpoint was not omitted: %x against %x", without, der)
	}

	if _, err := EncodeXIRI(rec(UEEndpoint(net.ParseIP("10.45.0.2")))); err == nil {
		t.Error("a list was accepted where the module defines a single CHOICE")
	}
}

// TestAZeroVolumeAndTMSIAreSent: a release that moved no bytes, and a 5G-TMSI of zero, are
// values, not absences. As plain integers they were omitted whenever they were zero.
func TestAZeroVolumeAndTMSIAreSent(t *testing.T) {
	zero := int64(0)

	der, err := EncodeXIRI(SMFPDUSessionRelease{
		SUPI: IMSI("262019876543210"), PDUSessionID: 5, UplinkVolume: &zero, DownlinkVolume: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hex.EncodeToString(der), "870100880100") {
		t.Errorf("zero volumes were not sent: %x", der)
	}

	der, err = EncodeXIRI(SMFPDUSessionRelease{SUPI: IMSI("262019876543210"), PDUSessionID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(hex.EncodeToString(der), "880100") {
		t.Errorf("absent volumes were sent: %x", der)
	}

	tmsi := FiveGTMSI(0)
	der, err = EncodeXIRI(AMFUEServiceAccept{
		UserIdentifiers:        sampleIdentifiers(),
		ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		FiveGTMSI:              &tmsi,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hex.EncodeToString(der), "840100") {
		t.Errorf("a zero 5G-TMSI was not sent: %x", der)
	}
}
