<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# TS 33.128 record conformance

**Made against 3GPP TS 33.128 V18.16.0**, and against the `TS33128Payloads.asn` module
published with it (`testdata/asn1/`). Both are cited below; where they disagree, the
disagreement is recorded, because one of the dispositions here lives exactly in the gap
between them.

## Why decoding is not enough

`golden_test.go` pins the encoder's output byte for byte, and every record's tags, optionality
and value bounds are audited against the text of the published module (`emit_tags_test.go`,
`asn1_drift_test.go`, `constraints_bounds_test.go`). Neither proves conformance:

- **The golden file pins this encoder's own output.** It catches an encoder regression. It
  cannot notice a field that is never populated.
- **A record that matches the module is well formed.** That does not prove it carries the
  fields TS 33.128 says it carries: the M/C/O markers live in the payload tables of clause 6.2,
  and they do not always agree with the module's `OPTIONAL` markers. `gTPTunnelInfo` is
  `OPTIONAL` in the module and **M** in three payload tables, so a record omitting it would
  decode cleanly and still be non-conformant.

So this document compares against the **payload tables**, not only the ASN.1.

## Scope

Bounded to the records a point of interception here emits, since a record nobody emits cannot
carry a wrong field to a mediation function.

| | Count |
|---|---|
| Record types defined in `li/iri` | 17 |
| Emitted by the AMF | 11 |
| Emitted by the SMF | 5 |
| Emitted by the UPF | 0 — the UPF is a CC-POI and produces no xIRI |
| Defined but not emitted | 1 — `AMFPositioningInfoTransfer` |

`AMFPositioningInfoTransfer` is defined so the set is complete against the module, and emitted
by no point of interception: every trigger event in clause 6.2.2.2.8 is an exchange with an
LMF, and the AMF here has no LMF integration. It becomes usable the day an LMF is wired up;
`li/README.md` carries the reasoning.

## Mandatory fields

Every field the clause 6.2 payload tables mark **M**, for the 16 emitted records, against the
Go definition in `iri.go`. `AMFPositioningInfoTransfer` also carries its mandatory `sUPI` and
`lcsCorrelationId`.

| Record | Payload table | Mandatory fields | Verdict |
|---|---|---|---|
| AMFRegistration | 6.2.2.2.2-1 | registrationType, registrationResult, sUPI, gUTI | All present |
| AMFDeregistration | 6.2.2.2.3-1 | deregistrationDirection, accessType | All present |
| AMFLocationUpdate | 6.2.2.2.4-1 | sUPI, location | All present |
| AMFStartOfInterceptionWithRegisteredUE | 6.2.2.2.5-1 | registrationResult, sUPI, gUTI | All present |
| AMFUnsuccessfulProcedure | 6.2.2.2.6-1 | failedProcedureType, failureCause | All present |
| AMFIdentifierAssociation | 6.2.2.2.7-1 | sUPI, gUTI, location | All present |
| AMFIdentifierDeassociation | 6.2.2.2.7-2 | sUPI, gUTI | All present |
| AMFRANHandoverCommand | 6.2.2.2.9.2-1 | userIdentifiers, aMFUENGAPID, rANUENGAPID, handoverType, targetToSourceContainer | All present |
| AMFRANHandoverRequest | 6.2.2.2.9.3-1 | userIdentifiers, aMFUENGAPID, rANUENGAPID, handoverType, handoverCause, pDUSessionResourceInformation, targetToSourceContainer, rANSourceToTargetContainer | All present |
| AMFUEPolicyTransfer | 6.2.2.2.12-1 | sUPI, uePolicy | All present |
| AMFUEServiceAccept | 6.2.2.2.13-1 | userIdentifiers, serviceMessageIdentity | All present |
| SMFPDUSessionEstablishment | 6.2.3-1 | pDUSessionID, gTPTunnelID, pDUSessionType, dNN, requestType, gTPTunnelInfo | All present |
| SMFPDUSessionModification | 6.2.3-2 | gTPTunnelInfo | All present |
| SMFPDUSessionRelease | 6.2.3-3 | sUPI, pDUSessionID | All present |
| SMFStartOfInterceptionWithEstablishedPDUSession | 6.2.3-4 | pDUSessionID, gTPTunnelID, pDUSessionType, dNN, requestType, gTPTunnelInfo | All present |
| SMFUnsuccessfulProcedure | 6.2.3-5 | failedProcedureType, failureCause, initiator | All present |

Every emitted record satisfies its mandatory set.

## Every unmodelled field, and its marker

`asn1_drift_test.go` enumerates the fields the published module defines for each emitted
record, and fails on any this package neither models nor records in `declaredAbsent`. Each of
the **116** recorded fields carries the M/C/O marker read from its payload table:

| Marker | Count | Meaning here |
|---|---|---|
| **M** | 0 | A field its payload table marks M is rejected from the disposition list outright: it is either populated or a known defect, never a disposition. |
| C | 115 | Conditional. Two are explicitly deprecated by the specification. |
| O | 1 | `AMFUEServiceAccept/deprecatedUERequestType`, itself deprecated. |

Three of the conditional fields are spelled differently in the tables than in the module —
`sMSoverNASIndicator` in the tables against `sMSOverNasIndicator` and `sMSOverNASIndicator` in
the module — and were resolved by reading both.

### Every condition, judged

A field marked C is a question, not an answer, so each recorded field carries a verdict on
whether this project meets its condition:

| Verdict | Count | Meaning |
|---|---|---|
| NOT HELD | 69 | the network function does not hold the datum |
| **MET** | **0** | the network function holds it and does not report it |
| N/A | 26 | the condition cannot arise; the feature is not implemented here |
| DEFERRED | 9 | held, but the subtree is deliberately not modelled |
| UNTRACED | 9 | a similarly named context field exists and the mapping was not followed through |
| BLOCKED | 0 | the encoder cannot express the value |
| DEPRECATED | 3 | the specification no longer uses the field |

`MET` and `BLOCKED` are kept at zero rather than removed: an empty verdict is a statement about
this implementation, while a missing row would read as a category that does not exist. A field
in either belongs in `asn1_drift_test.go`'s `knownConditionalDefects`, a separate list that
`TestNoKnownConditionalDefects` requires to be empty, so adding one is a deliberate act rather
than one more line among fields that are fine. The counts in both tables above are held to the
disposition list by `TestTheDispositionCountsAreTheDocumentedOnes`.

The two lists are checked differently. `declaredAbsent` is a claim about modelling, so an entry
for a field that becomes modelled fails the test. `knownConditionalDefects` is a claim about
what the network functions populate, and a field can be modelled and still not populated, so
modelling one does not clear it.

The nine `UNTRACED` verdicts are an admission, not a conclusion, and are labelled so they
cannot be mistaken for clearance: `pCCRules`, `pCCRuleIDs`, `oldPDUSessionID`,
`handoverState`, `uEPolicy` and `rRCEstablishmentCause` each have a plausibly corresponding
field in a network function's context that was not followed through to the point of
interception. One more sits below the records, outside these counts:
`FiveGSSubscriberID/sUCI`, a `UserIdentifiers` alternative `iri.Identifiers` does not build.

The 26 `N/A` verdicts rest on features this deployment does not implement: EPS/5GS
interworking over N26 and the SGW/PGW role, non-3GPP access through an N3IWF, TNGF or TWIF,
satellite backhaul, multi-USIM, and non-public networks.

## How the populated fields are populated

These are the fields where how a value is obtained decides whether the record is right. Each is
populated by the AMF or SMF that builds the record; what follows is what those builders must
do, and what this package enforces.

**`gTPTunnelInfo`.** Tables 6.2.3-1, 6.2.3-2 and 6.2.3-4 mark it `M` with cardinality 1; for
`SMFPDUSessionModification` it is the record's only mandatory field. It is modelled as
`GTPTunnelInfo`/`FiveGSGTPTunnels` and populated from the UL NG-U F-TEID the SMF also reports in
`gTPTunnelID`, which is what table 6.2.3-1C asks for. `additionalULNGUUPTunnelInformation` and
`dLRANTunnelInformation` are not modelled — the SMF holds neither for the single default path it
manages — and neither is `ePSGTPTunnels`, which reports PDN connection events at an SGW/PGW.

**`sUPIUnauthenticated`.** Table 6.2.3-1 says it "shall be present if a SUPI is present in the
message": `true` when the SUPI has not been authenticated and `false` when it has. `false` is the
ordinary value, so the member is a pointer — present exactly when non-nil — and
`TestPointerMembersAreThreeState` holds every pointer member to that. Absent means the record
carries no SUPI. It is not mandatory: a record carrying no SUPI must not assert an
authentication status for an identity that is not there.

**The identities.** `sUCI`, `pEI`, `gPSI`, `fiveGSTAIList` and `rATType` on the AMF records,
and `rATType`, `servingNetwork`, `aMFID` and `uEEndpoint` on the SMF records, are reported
wherever the module defines them and the network function holds the value:

- **`sUCI` is built from the NAS mobile identity's octets**, not from a string rendering of it,
  which loses the routing indicator's leading zeros. A SUCI whose members do not come out clean
  is omitted: a missing identity is a gap an audit can find, a wrong one is a record an agency
  acts on.
- **`schemeOutput` is characters.** Table 8.3.5-1 defines it as "the characters resulting as the
  output of the permanent identifier with the protection scheme applied". Under the null scheme
  those are the MSIN's digits, which NAS carries nibble-swapped, so they are converted rather
  than copied — copied, MSIN `0100007488` would go out as `1000004788`, a well-formed record
  naming a subscriber who does not exist. Under any other scheme the output is ciphertext and
  the octets stand. Only decoding a delivered record and reading the subscriber it names can
  catch the copied form: it is structurally valid, present and non-empty.
- **`fiveGSTAIList` is the registration area** — "tracking areas associated with the
  registration area within which the UE is current registered" — not the single serving TAI.
- **`aMFID` is the AMF identifier** of TS 23.003 clause 2.10.1: the AMF region, set and pointer
  of the AMF's GUAMI, not an NF instance identifier.
- **The IMEI is the fourteen-digit form**: TAC and serial number, without the check digit a
  TS 29.571 `imei-` PEI carries. This package refuses a fifteen-digit `IMEI`.

**Enumerations and zero.** Every TS 33.128 `ENUMERATED` this package carries is numbered from
one, and a neighbouring protocol's enumeration often from zero — NGAP numbers `handoverType` and
the cause groups from zero. A value cast across unmapped arrives as zero exactly when its first
member was meant, and a mandatory member carrying zero is a record a conformant decoder refuses.
So zero is refused in every mandatory enumerated member, and accepted as absence in an optional
one. `constraints.go` keys the rule by type where a type is mandatory wherever it appears, and by
record and field where it is mandatory in one record and optional in another:

| Type | Mandatory in | Guarded by |
|---|---|---|
| `HandoverType` | AMFRANHandoverCommand, AMFRANHandoverRequest | type |
| `CauseRadioNetwork`, `CauseTransport`, `CauseNas`, `CauseProtocol`, `CauseMisc` | AMFRANHandoverRequest (`handoverCause`) | type |
| `AMFDirection` | AMFDeregistration | type |
| `AMFRegistrationResult` | AMFRegistration, AMFStartOfInterceptionWithRegisteredUE | type |
| `AMFFailedProcedureType` | AMFUnsuccessfulProcedure | type |
| `SMFFailedProcedureType` | SMFUnsuccessfulProcedure | type |
| `PDUSessionType` | SMFPDUSessionEstablishment, SMFStartOfInterceptionWithEstablishedPDUSession | type |
| `Initiator` | SMFUnsuccessfulProcedure | type |
| `AccessType` | AMFDeregistration (OPTIONAL in four SMF records) | record and field |
| `FiveGSMRequestType` | SMFPDUSessionEstablishment, SMFPDUSessionModification, SMFStartOfInterceptionWithEstablishedPDUSession (OPTIONAL in SMFUnsuccessfulProcedure) | record and field |
| `AMFRegistrationType` | AMFRegistration (OPTIONAL in AMFStartOfInterceptionWithRegisteredUE) | record and field |

`TestEveryMandatoryEnumeratedFieldRefusesZero` derives the required entries from the module and
holds both maps to them, in both directions; `TestAnUnsetOptionalEnumeratedFieldStillEncodes`
holds the optional carriers to their absence.

Not guarded, because the module's range includes zero: `PDUSessionID`, `RoutingIndicator`,
`ProtectionSchemeID` (zero is the null scheme), `AMFPointer`, `AMFRegionID`, `AMFSetID`,
`FiveGMMCause` and `FiveGSMCause`.

**`PDUSessionType`** is the one guarded type an SMF builds by casting another protocol's value:
TS 24.501 numbers the PDU session type identically, and the SMF asserts that correspondence
member by member where it builds the record.

**`handoverCause`.** The five `HandoverCause` arms number their causes from 1, and NGAP's
corresponding groups from 0, so an AMF maps each cause by name, never by value; `causevalues.go`
names the TS 33.128 values. NGAP defines causes in `CauseRadioNetwork` and `CauseNas` that
TS 33.128 V18.16.0 has no equivalent for. Those are delivered as the group's own `unspecified` and
reported over X1 as `recordValueSubstituted` at element scope, because the field is mandatory and
an unreported handover is a larger gap than an imprecise one. The report is the only thing
distinguishing that `unspecified` from one the network gave itself; the record cannot.

**Strictness.** The module is declared `EXTENSIBILITY IMPLIED`. The checks in `constraints.go`
admit only the values the module itself defines; whether a receiver would tolerate a value
outside them is the receiver's matter, and an element that emits only defined values does not
depend on it.

## Open

### `location` is inconsistent across the AMF's records

The AMF populates a minimal `Location` (`locationInfo.currentLocation` only) in three records
and omits the field from `AMFRegistration` and `AMFDeregistration`, where the tables mark it
conditional on availability. The underlying gap is the unmodelled `userLocation` subtree — the
AMF has no reportable location detail to put there — so this is an inconsistency to resolve
alongside that subtree rather than a separate defect. Whichever way it is resolved, the five
records should agree.

### `UEPolicy`'s size constraint forbids the containers 5G actually carries — declared against TS 33.128

`AMFUEPolicyTransfer` (clause 6.2.2.2.12) makes `uEPolicy` mandatory, and the trigger for the
record is the AMF transferring a UE policy container. The type is defined as:

```
UEPolicy ::= OCTET STRING (SIZE(16..65540))
```

A UE policy container is a UE policy delivery service message (TS 24.501 Annex D). The
shortest is `MANAGE UE POLICY COMPLETE`: a procedure transaction identity and a message type,
one octet each (table D.5.2.1.1). This package refuses anything under 16 octets and accepts 16.
So the element must produce the record, and must not encode it; both cannot hold.

**What this package does: follows the schema, and says so.** The constraint is enforced, the
record is not delivered, and the network function reports the condition as a task-scoped
`recordNotEncoded` naming the warrant and the record type.

**Why not relax the lower bound.** A mediation function validating against the published module
discards a 3-octet `UEPolicy` exactly as this encoder does, so relaxing it would not deliver the
record — it would only move the discard to the far end, where this element cannot see it. An
element that knows it did not deliver is better than one that believes it did.

**Why not pad.** Padding invents wire content. The receiver cannot distinguish the invented
octets from the subject's own policy container.

**What an agency loses.** For a tasked subject, the short half of a policy exchange is not
delivered — typically the UE's `MANAGE UE POLICY COMPLETE`. The downlink command, which carries
the policy itself, is normally long enough to encode. The agency receives the substantive half
and a fault naming the warrant and the missing record type, rather than silence.

**What would resolve it.** A correction to TS 33.128's `UEPolicy` bounds, or a clause stating
that the field carries something other than the NAS container verbatim.

### `currentLoc` cannot be reported as `false` — latent

`LocationInfo.CurrentLocation` models `currentLoc [2] BOOLEAN OPTIONAL` as a plain `bool`, and
the emitter writes it only when it is `true`, so a `LocationInfo` saying its location is *not*
current cannot be expressed: `false` and absent encode identically.

It is latent rather than live: the AMF sets `CurrentLocation: true` in every record that carries
a `Location`, and no record here reports a last-known location. It becomes live the day a
location that is not current is reported. The remedy is the one `sUPIUnauthenticated` uses —
declare the field as a pointer, which the emitter writes exactly when it is non-nil. It is
recorded here because a field the encoder cannot represent is a known defect, not a presence
rule that happens to hold for the values seen so far.

## Where the module and the tables disagree

- **`gTPTunnelInfo`** is `OPTIONAL` in the module and **M** in tables 6.2.3-1, 6.2.3-2 and
  6.2.3-4. This package follows the tables and populates it.
- **`SMFStartOfInterceptionWithEstablishedPDUSession/uEEndpoint`** has no `OPTIONAL` in the
  module, while table 6.2.3-4 marks it **C**, 0..N, "if available". A session with no endpoint
  address (Unstructured, or Ethernet before a MAC is learned) is reported with the field
  written as an empty `SEQUENCE OF`, the module's only form for "not available". In the
  establishment and unsuccessful-procedure records the field is `OPTIONAL` and conditional, so
  an empty list is omitted.
- **`SMFPDUSessionModification/requestType`** has no `OPTIONAL` in the module, while table
  6.2.3-2 marks only `gTPTunnelInfo` **M**. The module is what a receiver validates against, so
  the zero guard follows the module; the SMF populates the field either way.
- **`CauseRadioNetwork`** runs 1..52 with **no value 28**, so it defines 51 causes where NGAP
  defines 58. Three of its names are spelled differently from NGAP's
  (`iMSVoiceeEPSFallbackOrRATFallbackTriggered`, `uPIntegrityProtectioNotPossible`, and
  `nPMAccessDenied` for NPN access); each sits at the position its NGAP counterpart predicts, so
  all three are read as the same cause. `enumConstraints` admits 28 because a range cannot
  express a hole, and an AMF's mapping must never produce it. (NGAP numbers its
  radio-network causes from 0, to 57 in the release the SD-Core AMF uses, so 58 values; TS
  33.128 numbers from 1.)
- **`FiveGSMCause`** is `INTEGER (0..255)` and mandatory in `SMFUnsuccessfulProcedure`, while
  TS 24.501's 5GSM cause values start at 8. The module's range is what a receiver validates
  against, so zero is not refused here; a tighter range would be a correction to TS 33.128, not
  to this encoder. The same holds for `FiveGMMCause` in the AMF.
