<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# TS 103 221-1 conformance disposition

**Made against ETSI TS 103 221-1 V1.21.1 (2025-08)**, and against the published
`TS_103_221_01.xsd` **v1.19.1** vendored under `testdata/schemas/`. Every clause and table
number below refers to that revision of the prose; every statement about element names,
ordering or value formats refers to that revision of the schema. Where the two could
disagree, the prose is normative for behaviour and the schema still decides what validates —
`testdata/schemas/README.md` carries that tension, and `SOURCES.md` carries the quoted
statements the provisioning decisions rest on.

When a newer revision is published, this document is what tells you whether the code
disagrees with the specification or merely predates it. That distinction is the reason the
versions are stated at the top rather than left implied.

## What this is, and what it is not

Unlike its two siblings, this disposition sits beside a **mechanical** check. X1 is the one
interface of the three for which ETSI publishes a machine-readable definition, and this
package uses it in both directions:

- **`TestRenderedResponsesValidate`** validates what this element *emits* against the
  published XSD. It catches a malformed answer.
- **`schema_drift_test.go`** compares the schema's own content models against the structs
  this package declares, and fails on an unmodelled element. It catches what this element
  fails to *read* — an unmodelled `xs:sequence` element would be silently discarded and the
  task acknowledged with the narrowing it carried thrown away.

So the questions this document answers are the ones neither check can reach: **which of the
specification's messages this element implements at all, what it refuses and with which code,
and where a deliberate decision diverges from the prose.** A schema cannot say that a message
type is unimplemented, and it cannot say that a "shall" in clause 6 has no code behind it.

Do not close a question about this package by citing this document alone. It is a reading,
and it is only as current as the revisions it names.

## Deliberate deviations

The places where this element departs from the letter of TS 103 221-1, each for a stated
reason. Everything else in this document is a disposition, not a deviation.

- **`RemoveAllDestinations` is disabled by default.** Clause 6.3.4 enables it unless agreed
  otherwise, as it does `DeactivateAllTasks`. Bulk deactivation keeps the clause's default,
  because stopping every interception fails safe; bulk destination removal is off until a
  deployment enables it (`WithRemoveAllDestinations`), because it strands an element that is
  still tasked with nowhere to deliver. See *Destinations* below.
- **`CorrelationID` is acted on by the triggered CC-POI**, which clause 6.2.1.2 says a
  non-mediation NE "shall" ignore. TS 33.128 table 6.2.3-6 makes it mandatory on the LI_T3
  trigger and the means of joining content to signalling; the reasoning and both quotations
  are in `testdata/schemas/SOURCES.md`.
- **The emitted Version is `v1.6.1`** while the behaviour is built against v1.21.1 — a knowing
  understatement, explained under *Message envelope*.

These follow the specification and are recorded here because each is a choice a reader may
check:

- **A task's target identifiers must all match** (table 6.2.1.2-1: "Where multiple criteria
  are present, all criteria are required to be matched"). A task naming an identifier the
  element does not evaluate, or a combination no subject or packet satisfies — two SUPIs, two
  sessions, two directions, two port criteria — is refused (3000/3001) rather than narrowed.
- **An ActivateTask for an XID already held** is acknowledged when it is identical to the held
  task, so a retried activation stays idempotent, and refused with 2010 when it differs:
  clause 6.2.1 makes a held XID an error, and a change goes through ModifyTask.
- **A task-scoped fault is reported as cleared** when its condition ends (clause 6.5.2.1), and
  the keepalive procedure follows clause 6.6.2 and is enabled by default. Both are described
  under *Issue reporting* and *Pings and keepalives* below.

## Message envelope — table 6.1-1

| Field | M/C/O | Disposition |
|---|---|---|
| ADMF Identifier | M | Emitted on every message. On inbound, bound per message to the configured responsible ADMF **and** to the peer's X.509 certificate (clause 8.2.4) — 1030 on a certificate mismatch, 1040 on an unexpected ADMF |
| NE Identifier | M | Emitted from configuration. On inbound, a message naming a different NE is refused with 1060 |
| MessageTimestamp | M | Emitted as the TS 103 280 Qualified Microsecond Date Time, rendered to a fixed six digits — see `li/README.md`, *X1 timestamps are rendered to a fixed six digits*, for why that is a conformance decision and not a formatting one |
| Version | M | Emitted as `v1.6.1` on every message this element encodes, responses included: table 6.1-1 defines it as the version used to encode the message, not an echo of the request's. On inbound, a version other than `v1.x.y` is refused with 1020. Read below |
| X1TransactionID | C | Echoed on every response. Generated per request when this element is the requester. Substituted when a peer's value is outside the TS 103 280 UUID format — see *Echo substitution* |

**The Version this element originates is `v1.6.1`, and the behaviour is built against v1.21.1.** This is a
knowing understatement rather than a claim: clause 4.5 makes minor increments backwards
compatible, so declaring an older revision than you implement is the direction that cannot
mislead a peer into expecting a message you do not answer. The reverse would be. It is
recorded because "the version field says 1.6.1" is otherwise indistinguishable from an
element that was built against v1.6.1, and this one was built against v1.21.1. How the
version of an answer this element receives is checked is under *This element as an X1 client*.

**Echo substitution.** Every response echoes the request's `x1TransactionId`, and the schema
restricts it to a UUID. A peer sending a value outside the restriction would otherwise make
*our own* answer schema-invalid — and the answer easiest to spoil that way is the one whose
request has been trusted by nothing: the refusal telling a peer it may not task this element.
So a transaction identifier outside the TS 103 280 UUID format is replaced by a fresh UUID.

The substitution costs something and it is worth saying who pays. An ADMF matches a
response to its request by that value, so a peer that sent a non-conformant identifier loses
the correlation — but it had no conformant correlation to lose, and echoing the malformed
value would invalidate the whole response, losing the correlation *and* everything else the
message says. A conformant ADMF's value is echoed untouched. A fresh value is chosen rather
than a fixed one so that two refusals to the same peer stay distinguishable.

**A consequence for anyone validating responses**: against a requester that sent a
non-conformant identifier, a conformant server is *required* to answer with a different one.
A strict equality check on the echo is therefore sound only for a requester that always
generates a conformant identifier.

## Tasks — clause 6.2

| Message | Disposition |
|---|---|
| `ActivateTask` (6.2.1) | Implemented |
| `ModifyTask` (6.2.2) | Implemented, mid-interception. Criteria and products may both change; the task is not torn down |
| `DeactivateTask` (6.2.3) | Implemented. An XID the element does not hold is refused with **2020**, per table 6.2.3-2: "it is an error if the XID is not already present at the NE". Not 1020, which is "Unsupported version" |
| `DeactivateAllTasks` (6.2.4) | Implemented, **enabled by default**, which is the specification's own default: "By default (if there has been no agreement in advance) then DeactivateAllTasks is enabled" |

**Task fields refused rather than narrowed.** The rule the element applies is that a field it
cannot honour refuses the task, because acknowledging it would tell the provisioning function
an interception is running that is narrower than the one ordered — and nothing downstream can
discover that:

| Field | Marker | Disposition |
|---|---|---|
| `listOfServiceTypes` | O | Refused, **3050** |
| `listOfTrafficPolicyReferences` | O (annex F) | Refused, 3000/3001. Declared permanently unsupported — see `li/CONFORMANCE.md` |
| `taskDetailsExtensions` other than the LI_T3 one | — | Refused. An extension exists in order to change the meaning of the message carrying it, so an unknown one cannot be ignored. Includes `HeaderReporting`, which TS 33.128 marks C and M in tables 6.2.3-0A and 6.2.3-9 |
| `listOfMediationDetails` | O | Accepted and disregarded, deliberately — `li/README.md`, *Fields accepted and disregarded* |
| `implicitDeactivationAllowed` | O | Accepted and disregarded, likewise |
| LI_T3 `PDRID` and `QERID` | — (TS 33.128 table 6.2.3-7) | Mapped to criteria by this package; **refused by the UPF CC-POI** (its `CanApply`), 3000/3001. Both name a rule allocated *per PFCP session* and reused across sessions from a low number, so on its own a rule ID names a rule in every session that holds that number. A session could qualify them, since a task's criteria are conjunctive, but each criterion is judged alone at activation, so they are refused whatever accompanies them. A triggering function needing one session's traffic has `FSEID` |
| LI_T3 `PDR` whose rule leaves a field to the element | — (same table) | **Refused by the UPF CC-POI** where the encoded rule asks the UPF to allocate the UE address or to choose the tunnel endpoint (`CH`). Such a criterion names a value the UPF assigns, so it can never compare equal to any session's rule: accepted, it is an interception acknowledged and producing nothing |

**Identifier formats.** `xId`, `dId` and `productID` are all `etsi103280:UUID` in the schema.
A value outside that format is refused with **1010** rather than stored and acted on: an
element that accepts a malformed identifier interoperates with a provisioning function no
conformant one would produce, and hides format defects on both sides until they surface as a
mismatch neither party can attribute.

### Identifier cardinality

`TargetIdentifier` is an `xs:choice` in the schema, and `UPFLIT3TargetIdentifier` is a
sequence of choices. Two decisions about how many members of each are honoured are recorded
here because they are conformance statements, and this file is what is distributed with the
code.

**A `TargetIdentifier` populating more than one arm is refused with 1010**, as the schema
violation it is. A message carrying two arms is invalid against the `xs:choice`, so no reading
of it is authoritative, and selecting one would mean the *element* deciding the scope of an
interception the provisioning function ordered. It is a **schema** error rather than a task
this element cannot honour, and the existing ordering — malformed checked before unhonourable
— puts it in the right place: asking whether the contents could be honoured presumes a
well-formed message.

The arms are counted over every field of the structure — unmodelled ones included — rather
than in the order the mapping switch happens to try them: a guard sharing that order would
leave the cardinality unchecked wherever the switch stopped first.

**The same rule applies to the inner choice.** `UPFLIT3TargetIdentifier` is a sequence *of*
choices, so several members are valid and several arms of one member are not — and the reason
is unchanged: a member with two arms has no authoritative reading, so selecting one narrows or
widens an interception by this element's decision rather than the provisioning function's.
Also 1010, naming which criterion of how many. Checking the outer choice and not this one
would leave the identical gap at the one level where a CC-POI actually reads its detection
criteria.

**This does not forbid a task from carrying several target identifiers.** A task's
`targetIdentifiers` list may name several, and they are conjunctive: TS 103 221-1 table
6.2.1.2-1, "Where multiple criteria are present, all criteria are required to be matched." An
element refuses (3000/3001) a task naming an identifier it does not evaluate, or a combination
no subject or packet satisfies — two SUPIs, two sessions, two directions, two port criteria.
What is refused as a schema error (1010) is two arms inside *one* identifier. The two rules are
about different levels of the structure.

**Every member of a `UPFLIT3TargetIdentifier` list becomes a criterion.** It is a SEQUENCE OF
CHOICE, so a list of several is what the structure is for, and all of a task's criteria are
required to match (table 6.2.1.2-1). A member that cannot be mapped
**refuses the whole task**, naming which member of how many — the rule applied to the outer
list, applied one level deeper. An empty list is refused. Mapping only some members would
acknowledge a task while running an interception narrower than the one ordered, invisibly to
every party: the triggering function would be told the task was accepted, and the mediation
function would receive well-formed product for the criteria that survived.

## Destinations — clause 6.3

| Message | Disposition |
|---|---|
| `CreateDestination` (6.3.1) | Implemented. `dId` validated as the schema's UUID; re-creating an existing DID refused with **2030** per clause 6.3.1.1 |
| `ModifyDestination` (6.3.2) | **Not implemented**, refused with 1080. A destination is corrected by `RemoveAllDestinations` once no task references it |
| `RemoveDestination` (6.3.3) | **Not implemented**, refused with 1080 |
| `RemoveAllDestinations` (6.3.4) | Implemented, **disabled by default** — a deliberate deviation from clause 6.3.4, which enables it unless agreed otherwise. Answered with the specification's own error, 8020, when disabled. When enabled, refused with **8010** while any destination is still referenced by a task, which is the guard the specification itself puts on bulk removal |

The asymmetry between bulk deactivation (on by default, as clause 6.2.4 has it) and bulk
destination removal (off by default, against clause 6.3.4) is this element's choice.
Deactivating everything fails safe — interception stops, which is the direction this
capability fails in anyway. Removing every destination is not symmetric with that: it strands
an element that is still tasked and now has nowhere to deliver, so it waits for a deployment
to agree to it.

**`deliveryAddress` must be an IP address and port.** A URI, an E.164 number or an email
address is refused with **6020**, the registry's own entry for an unsupported delivery address
type.

**Destination sets (`dSId`)** are refused. A set is a `DestinationSetDetails` Generic Object
(annex E) and this element implements none, so a `dSId` can never name anything it holds while
carrying failover and duplication semantics an acknowledgement would silently promise.
Declared permanently unsupported — see `li/CONFORMANCE.md`.

## Interrogation — clause 6.4

**All eight messages clause 6.4.1 makes mandatory are implemented**: `GetTaskDetails`,
`GetAllDetails`, `GetAllTaskDetails`, `GetDestinationDetails`, `GetAllDestinationDetails`,
`GetNEStatus`, `ListAllDetails`, `GetAllGenericObjectDetails`.

These properties of the answers matter more than the coverage:

- **Holding nothing is a successful empty answer, not an error.** An ADMF reconciling after a
  restart must be able to tell "this element holds no tasks" from "this element would not
  say".
- **`GetAllGenericObjectDetails` answers with its object list omitted**, not empty. That is
  how the standard states that Generic Objects are unsupported — tables 6.4.6.1-2 and
  6.4.9.1-2 mark the lists **C** and say they "may be omitted if Generic Objects are not
  supported by the NE". An empty list would claim the capability is implemented and none is
  held.
- **`GetNEStatus` answers for the conditions the element can currently observe**, and stops
  reporting each when it stops holding — `mdfUnreachable` while delivery is failing,
  `x3EgressDown` while the datapath egress is down.
- **`destinationDeliveryStatus` answers from the delivery layer**, not from a constant.
  `deliveryFault` while the element cannot reach that destination's endpoint,
  `activeAndWorking` while it can, computed per request and never cached — so a destination
  that recovers is reported as recovered without anything having to clear a stored flag. The
  answer comes from the same reachability state `ReportDestinationIssue` reports from, so an
  element that has just told the ADMF an endpoint is unreachable does not answer "working"
  when the ADMF checks. An element that supplies no reachability answer at all reports
  `activeAndWorking`, as a stated default.

`GetTaskDetails` round-trips a task's target identifiers back into the vocabulary the request
used, across every identifier type this element models, so an ADMF can compare what the
element holds against what it believes it sent.

A device is matched by its fourteen-digit IMEI, whichever element names it: `peiImei` and
`imei` carry the IMEI, and `peiImeiCheckDigit` carries it with its TS 23.003 check digit,
which is verified and re-derived in the answer. A wrong check digit is schema-valid, so it is
refused as the operation's failure (3000 on activation) naming the check digit; a value of
the wrong length is outside the type and refused with 1010.

### What the status answer does not cover

`GetNEStatus` answering "the conditions the element can currently observe" is only half a
statement. **The conditions it does not answer for are named here**, so that "no faults" is a
stated answer rather than an omission. This section describes the UPF CC-POI, whose answers
these packages carry; the behaviour it describes is the UPF's.

**The UPF CC-POI does not answer for a disagreement between its record of what it programmed
and what the datapath actually holds.** It cannot: the UPF's BESS `ExactMatch` module carrying each
FAR's duplication flag accepts `add`, `delete`, `clear` and `set_default_gate` and offers no
read, so the element's own record is the only copy of that state on the control-plane side.

Where a rule is programmed into the datapath but not recorded, content can be duplicated with
no task covering it, and **an interrogation of this element will answer that no fault holds**,
because by its own record none does. The copies are dropped as unattributable rather than
delivered, so no agency receives them; the condition is invisible from both ends.

Three things bound it, and none of them makes it observable:

- A monotone set of every FAR ever told to duplicate means that whenever duplication is
  re-derived, "off" is pushed for such a FAR whatever the record claims.
- A re-derivation is triggered by any session event touching such a FAR, not only by a tasking
  change, so an ordinary session event corrects it.
- The correction is **blind**: it pushes defensively and never learns whether the datapath had
  diverged, so the element cannot report the moment a divergence *ends*.

A divergence therefore persists until the next event touching that session. For an idle,
long-lived session there may be none.

**The onset is observable where the element itself caused the uncertainty.** A rule write that
does not complete within its deadline is the element's own send path: it knows the batch was
unconfirmed and whether the rules in it carried duplication. Of the three reporting routes,
two are therefore implemented:

| Route | Available | State |
|---|---|---|
| Answer at **element** scope in `GetNEStatus` — a record/datapath disagreement in general | **no** — needs a read the BESS module does not offer | declared, open |
| Answer at **task** scope, where this element itself created the uncertainty | **yes** | `duplicationNotProgrammed` |
| Report the onset when it happens, over the push mechanism | **yes** | `duplicationRefused` |

An unconfirmed write is recorded as *not* duplicating rather than as the value intended, so the
next re-derivation rewrites the rules instead of skipping them, and an interrogation of the
task answers `duplicationNotProgrammed` instead of claiming the task is faultless.

**One asymmetry is declared rather than fixed, and it is load-bearing.** The pass that
re-derives duplication distrusts its own record **only when the answer is "off"**: a rule
recorded as not duplicating, which this element has ever turned on, is re-pushed whatever the
record claims. A rule recorded as *duplicating*, where the tasking agrees, is skipped.

That is deliberate and it is the right way round — over-collection is the graver harm and must
never be permanent. The cost is that the opposite failure is not self-correcting from the
record alone: an element that records a rule as duplicating when it does not know whether the
write landed would skip that rule on every later pass, for the life of the session, and answer
that the task is faultless throughout. Recording an unconfirmed write as not duplicating closes
that on every path that arises today; any new caller recording a write it could not confirm
must do the same, because nothing in the pass would catch it.

Closing the general case needs a read path into the datapath, which needs a command the BESS
module does not have. It is declared here because a condition whose observation and
classification cannot be made to agree must be named, not omitted.

## Issue reporting — clause 6.5

| Message | Disposition |
|---|---|
| `ReportTaskIssue` (6.5.2) | Implemented, with the closed `TaskReportType` enumeration and the clause 6.7 issue codes |
| `ReportDestinationIssue` (6.5.3) | Implemented, naming the DID. See *Scope, and the ending of a fault* |
| `ReportNEIssue` (6.5.4) | Implemented, with the closed `TypeOfNEIssueMessage` enumeration — `Warning`, `FaultCleared`, `FaultReport`, `Alert` — and issue codes from table 6.7-3 |
| `TopLevelError` (6.1) | Implemented: an unparseable request is answered with `X1TopLevelErrorResponse` and its four fields, at HTTP 200 per clause 7.2.2.2 |

**Reports are throttled to one per condition per 30 seconds** (see *Scope* below for what a
condition is), and this is a conformance
statement rather than a performance one: an ADMF that receives one report of a persistent
fault has been told about it, and an ADMF flooded off the interface has not. It also means a
condition that recurs inside the window is reported once, so a report is evidence that a
condition occurred and not a count of occurrences.

**A failed report has nowhere to go.** The LI plane must not surface faults through any
channel but this one, so a report that cannot be delivered is discarded rather than logged.
That is deliberate — writing the error anywhere general would be the disclosure this plane
exists to avoid — and it means the fault channel is best-effort by construction.

### Scope, and the ending of a fault

Clause 6.5.1 scopes an issue three ways — to a task, to a delivery destination, or to the
whole network element — and **the scope is what tells a provisioning function where to act**.
Reported at element scope, an unreachable mediation function would tell an ADMF that had
provisioned several destinations that one of them was unreachable, and not which.

A destination-scoped fault is reported as one, naming the DID. **Naming it is not a
widening of what this channel discloses**: it is the provisioning function's own identifier
for an endpoint it created, and it names neither a target nor a warrant. Where one endpoint
serves several provisioned identifiers, the fault is reported for each — the provisioning
function's unit of action is the destination it created, and reporting per endpoint would
require it to know how this element resolves them.

**A fault that ends is reported as having ended**, per clause 5.3 — "The NE shall also
indicate that a fault has been cleared (see clauses 6.5.2 and 6.5.3) unless otherwise
configured" — as `AllClear` at task and destination scope and `FaultCleared` at element scope.
A `FaultCleared` carries the status/fault code of the fault it clears (table 6.5.4-1).

Task-scoped faults that are states (`duplicationNotProgrammed`, `noTrafficSelected`,
`triggerNotRunning`) are reported as `NonTerminatingFault` when they appear and retracted with
`AllClear` once absent for one sampling interval (clause 6.5.2.1). Task-scoped events
(`recordNotEncoded`) are reported once and have no ending. The faults of a task that is
deactivated are not retracted: the deactivation ends them.

Three properties of how that is done, because each is a place the obvious implementation is
wrong:

- **One party owns both edges.** Reachability is re-observable — every supplier answers from
  state its senders already hold — so an ending is detectable, which is what makes the
  clearing report possible at all. Nothing in the delivery layer signals recovery, and a
  recovery callback beside the failure one would put edge detection at every delivery site
  in every network function, each free to disagree about what "recovered" means. An element
  where one party announces and another retracts eventually announces something nobody
  retracts.
- **The report is not delayed by sampling.** The sites that notice a delivery failure ask the
  watcher to sample at once, so first notice does not wait for the next sampling interval.
- **A fault that cannot be re-observed gets no clearing report.** A destination that could
  not be *prepared* is a credential or configuration fault rather than a reachability one,
  and a fault that cannot be observed to hold cannot be observed to end. Those are still
  reported where they are noticed, at element scope. This is the existing
  event-versus-condition rule applied, not a second mechanism.

**Clause 5.3 also says "The NE shall remember which of the XIDs are in fault and whether the
NE itself is in a fault situation", and that does not conflict with this element's status
answer being recomputed.** What is remembered is *what has been said to the provisioning
function*, which is the only thing a clearing report can be derived from — knowing a fault
cleared requires knowing it was previously set, and no amount of re-observing the present
supplies that. The status answer remains determined from what is observable when it is asked
and remains not a history of what was reported. These are the standard's own two mechanisms,
answering "what changed" and "what holds now", and each is insufficient alone.

**Report rate limiting is scoped to the condition, not to the message type.** Keyed by issue
type alone, two destinations failing inside one window would be one report, and whichever
failed first would hide the other. The limit also does not apply across a state change: a fault beginning and that same fault clearing are two events, not a repetition, and
throttling the second against the first would report a fault this element never retracts.

### Withdrawal durability

How this element keeps a withdrawal from being lost has X1-visible consequences, recorded here
because there is nowhere else in a distributed document that they appear.

- **`OnTaskChange` is the lifecycle callback: one event per transition**, carrying the task as
  it was and as it becomes. A `ModifyTask` keeps the XID, so as two events "the old task" and
  "the new task" would share a key, and a POI that installed state for the new task and
  removed state for the old under it could reclaim what the installation had just created.
- **An exact replay is not a transition and fires nothing.** An `ActivateTask` repeating the
  task held under its XID is acknowledged and changes nothing, so a retried activation stays
  idempotent and does not re-emit records reporting the beginning of an interception that never
  stopped; a different task under a held XID is refused with 2010 (table 6.2.1.1-2: "it is an
  error if the XID is already present at the NE").
- **`OnPurge` names its reason** — `PurgeWithdrawal`, `PurgeBulkDeactivate`,
  `PurgeKeepaliveLapse` — because only the third is unasked-for. A purge an ADMF requested and
  a purge caused by that ADMF falling silent are the same event to the element and completely
  different events to an operator.
- **`taskingWithdrawalFailed` and `taskingWithdrawalStuck` are separate conditions.** The
  first says the last attempt to withdraw failed; the second says authority was removed some
  time ago and content is probably still flowing. Repeating the first would never say the
  second, and the operator action differs.
- **Withdrawals are retried rather than forgotten**, and the endpoint keeps receiving
  keepalives while a withdrawal is pending — deliberately, because a withdrawal in flight is
  tasking this function is still answerable for.
- **`reconcileFailed`** is the same ignorance arrived at from a restart rather than from a
  refused instruction, and is reported distinctly for the same reason.

## Pings and keepalives — clause 6.6

`Ping` (6.6.1) and `Keepalive` (6.6.2) are both implemented, and so is the clause 6.6.2
procedure for an ADMF that falls silent. When TIME_P2 (default 1 h) passes with no X1 request,
the element sends a ReportNEIssue `FaultReport` with code 9050 and starts TIME_P1 (default
1 min). An OK answer within TIME_P1 starts TIME_P3 (default 2 h); when TIME_P3 expires, or
TIME_P1 expires with no OK answer, the element deactivates all tasks and sends an `Alert` with
code 10000. Any X1 request ends the procedure, with a `FaultCleared` carrying 9050 when the
`FaultReport` was delivered. An element configured to retain tasking restarts TIME_P2 instead.
The procedure is enabled by default; a network function disables it only by configuration, and
timers outside what the procedure allows are refused rather than silently ignored.

**Only an authenticated message resets TIME_P2 or ends the procedure.** Unauthenticated traffic must not, or
anyone able to reach the X1 port could hold the fail-safe open indefinitely while the real
ADMF is gone.

## Protocol errors — clause 6.7

Table 6.7-3 instructs implementers to use the most specific code they can, and this element
does. The codes it emits, and what each means here:

| Code | Meaning | Where |
|---|---|---|
| 1000 | Generic error | The fallback for a refusal no more specific code fits. Every path this element knows names a specific one |
| 1010 | Syntax/schema error | A message violating its own schema: a missing mandatory element (`xId`, `dId`, `taskDetails`, `destinationDetails`); an identifier or target value outside the format the schema defines; a `deliveryType` outside its enumeration; an IPv6 destination address with a zone or in IPv4-mapped form; a `targetIdentifier` populating more than one arm of the schema's choice; a request carrying no message (answered as a `TopLevelError`) |
| 1020 | Unsupported version | A message whose version is not a `v1.x.y`: another major version, or a value outside the schema's pattern. The description names the version this element supports |
| 1030 | ADMF Identifier does not match certificate details | Clause 8.2.4 binding, and any message arriving without a client certificate |
| 1040 | Unexpected ADMF Identifier | A peer in the LI domain that is not this element's responsible ADMF |
| 1060 | Unexpected NE Identifier | A message addressed to a different element |
| 1080 | Unsupported request | An unimplemented message type, and the Generic Object CRUD |
| 2010 | XID already exists on NE | An `ActivateTask` naming a held XID with a different task; an identical replay is acknowledged |
| 2020 | XID does not exist on NE | `DeactivateTask`, `ModifyTask` or `GetTaskDetails` for tasking not held |
| 2030 | DID already exists on the NE | `CreateDestination` re-creating a DID |
| 2040 | DID does not exist on the NE | `GetDestinationDetails` for a DID not held; a task naming a destination identifier the element cannot resolve |
| 3000 / 3001 | Generic ActivateTask / ModifyTask failure | Task details this element cannot honour and has no more specific code for, including an IMEI whose check digit does not match |
| 3050 | Unsupported ServiceType | `listOfServiceTypes` |
| 5010 | DeactivateAllTasks not enabled | `DeactivateAllTasks` on an element configured without it |
| 6000 | Generic CreateDestination failure | A destination this element declines to create with no more specific code: an unrecognised destination extension, a DID declared in configuration and referenced by an active task, or a `UDPPort` of 0 (inside its type, but naming no endpoint) |
| 6020 | Unsupported DeliveryAddress type | A URI, E.164 number or email address |
| 8010 | Destinations in use | `RemoveAllDestinations` while a task still references one |
| 8020 | RemoveAllDestinations not enabled | `RemoveAllDestinations` on an element that has not enabled it, the default |

**Malformed is checked before unhonourable**, and the ordering is deliberate: a message that
violates its schema has no authoritative reading at all, so asking whether the element could
honour its contents presumes something not established.

## Generic Objects — clause 6.8

The whole capability is **refused, not acknowledged**. `CreateObject`, `ModifyObject`,
`GetObject`, `DeleteObject`, `ListObjectsOfType` and `DeleteAllObjects` all return 1080.

This is the conditional half of the pair: clause 6.8.6 makes `DeleteAllObjects` conditional on
supporting Generic Objects, and clause 6.8.1.1 provides for an NE that cannot store an object
type. The *mandatory* half — the `GetAllGenericObjectDetails` query — is answered
successfully with the list omitted, as above. Declared permanently unsupported; the reasoning
is in `li/CONFORMANCE.md`.

## Peer authentication — clause 8

Mutual TLS from an X0-provisioned LI PKI, with the peer certificate checked **per message**
rather than per connection: a certificate proves membership of the LI domain and not which
element the peer is, so clause 8.2.4's identity binding is what says this particular peer may
task this particular element. An attempt that fails it is refused silently and reported to the
ADMF as `x1AuthFailed` — nothing is malfunctioning, but somebody inside the LI trust domain is
attempting to task or untask network elements, and that channel is the only place it can be
said.

**One responsible ADMF per network element.** A second identity is refused.

## This element as an X1 client

The SMF acts as a CC Triggering Function over LI_T3, which makes it an X1 *requester* against
the UPF's CC-POI. Three dispositions belong to that direction.

**The destinations a trigger names are the task's, not the triggering function's.** A CC-TF
resolves the warrant's own X3 destinations, provisions each distinct endpoint at the POI with
`CreateDestination`, and names those identifiers on the trigger. Its configured `mdf3` serves
only a task that names no X3 destination the element can resolve — the same three-source
precedence, and the same fallback, that the IRI path applies to `mdf2`.

Naming the triggering function's own configured endpoint on every trigger instead would be
invisible with a single agency. With two, both agencies' **content** — the subscriber's own
traffic, not metadata — would arrive wherever configuration happened to point, crossing from
one agency to another, which no product type and no delivery path may do.

**A change to a task's labelling reaches the triggers already installed for it.** Where a
`ModifyTask` changes the `productID` a warrant's product is labelled with, its correlation
value, or its X3 destinations, the CC-TF sends a `ModifyTask` to each POI holding one of that
warrant's triggers, restating the session's own detection criterion and correlation. The
trigger is not withdrawn and reinstalled: content the warrant still authorises is not
interrupted to change a label. An IRI-POI reads these from the task per record and so picks
them up at once; a triggered POI reads them from a trigger built once, and without this the
two diverge — signalling under one warrant identifier and content under another, both
well-formed, with nothing in either stream to show they were meant to join.

**Every response is bound to the request that produced it** — the third disposition, and the
one the two above both lean on when they fail. In one validator both response readers go
through. Refused unless all of the following hold:

| Check | Why |
|---|---|
| Exactly one message | Every request this element sends asks one question; clause 6.1 makes a ResponseContainer "all the responses to the requests in the container". Taking the first of several would be choosing which answer to believe |
| The response type is the request type's | Catches a peer answering a different question. An `ErrorResponse` is admitted, since it is a legitimate answer to any request |
| `x1TransactionId` equals the one sent | This message is the answer to *this* request |
| `neIdentifier` is the element addressed | The only check that can detect a misroute |
| `admfIdentifier` is this element | The client-side form of the binding the server already performs |
| The version is a v1.x.y | Table 6.1-1 defines Version as the version the sender used to encode the message, so a peer answers in its own version rather than echoing the request's, and clause 4.5 keeps minor increments compatible. A major version this element does not speak is refused |

Outbound X1 requests do not follow HTTP redirects; a redirect is a failed request. An answer to
an outbound request is read up to 16 MiB; a larger one is refused.

Every field checked is a member of the schema's `X1ResponseMessage` base type, so every
response type carries all of them and each check can be **required** rather than applied only
when present. That was measured against the responder rather than read off the schema alone,
because a required check against a field a peer never sends is a permanent refusal by
construction — and on the withdrawal path a permanent refusal holds open both the withdrawal
and the fail-safe that would otherwise have completed it.

**Deliberately not required: an acknowledgement.** `oK` is *not* on the base type, and a
`GetAllDetailsResponse` carries none — it carries `neStatusDetails` and three lists. A
validator requiring one would refuse every details answer this element receives, which on a
reconciliation path means never learning what a POI holds. It is checked by the callers whose
own response type defines one.

**A validation failure is reported distinctly from a peer's refusal**, as
`x1ResponseUnattributable`. A refusal is a task-level condition the triggering function can
attribute to a warrant; an answer that cannot be bound to its request is element-level,
because which task it concerned is exactly what has not been established. The distinction is
not cosmetic on the withdrawal path: a systematic mismatch produces a pending entry that never
clears, and pending entries keep their endpoint's keepalives flowing, so the POI's own
fail-safe cannot reclaim the tasking either. Reported only as `taskingWithdrawalFailed`, that
presents as a POI at fault while the POI is answering perfectly well and being disbelieved
here — and the operator action is the opposite one.

Two limits on what the binding achieves, stated so it is not credited with a property it has
not got:

- **Three of the four identity fields are echoes**, copied by a conformant peer straight off
  the request, so any endpoint that received the request can return them correctly. They
  detect a *confused* answer, not a lying one. Only `neIdentifier` is the peer's own assertion
  of who it is, and an endpoint that wants to be believed states whatever the requester
  expects. Mutual TLS and the clause 8.2.4 binding are what bound the lying case; this does
  not extend them.
- **A response's `messageTimestamp` is deliberately not checked for freshness.** It would need
  a clock comparison and a tolerance, both deployment questions, to defend against replay
  inside the LI domain by a certificate holder — a threat this element does not otherwise
  address. Recorded so the omission reads as a decision.

**An issue report counts as delivered only on the responsible ADMF's word.** The answer to a
`ReportNEIssue`, `ReportTaskIssue` or `ReportDestinationIssue` is bound like the answer to any
other request this element sends — the peer's certificate, both identifiers, the transaction
identifier and the response type — so an endpoint that is not the ADMF cannot acknowledge a
fault report, and a refusal inside an HTTP 200 is not recorded as delivered.

**The peer that answers is this package's own `Server`.** Worth stating, because it bounds
both the risk and the evidence. In SD-Core the `Requester` is used by the SMF's CC triggering
function, pointed at a UPF's LI_T3 endpoint, and a UPF runs this server. The reference
simulator used for interoperability is an ADMF and MDF: an X1 *client* toward these network
functions, never the responder to a request they send. The residual is that the endpoint URL is deployment configuration, so a
third-party CC-POI is possible in principle; none is exercised here.

## What this disposition does not cover

An audit that finds nothing is not proof there is nothing, and this one is a reading. Stated
plainly, so nobody mistakes its scope for a clean bill of health:

- **Only the messages this element implements were read closely.** A message refused with 1080
  was checked for whether refusing it is permitted, not for what implementing it would entail.
- **Annexes were read only where a decision depended on one** — annex E for destination sets,
  annex F for traffic policies. The rest were not.
- **The schema checks cover form, not meaning.** `TestRenderedResponsesValidate` and
  `schema_drift_test.go` between them catch a malformed answer and an unmodelled element.
  Neither can notice that a "shall" in clause 6 has no code behind it; this document is what
  reads for that.
- **Nothing here was verified against a strict third-party ADMF.** The interoperability
  evidence is against the reference simulator and against these packages' own endpoints. A gap
  that only a strict validator would reject has not been exercised.
- **The client direction is tested against this package's own `Server` and against stubs.**
  The stubs answer from the wrong element, in the wrong version or with the wrong response
  type, and each is refused; no third-party responder exists to test against.

## A held task's own faults

`taskStatus` carries a `listOfFaults` computed when the question is asked, from what the
element supplies through `WithTaskFaults` — not a fault state stored on the task, which would
go stale and need clearing. At the content POI it answers `duplicationNotProgrammed` for a task
whose duplication is not in place, and a fault for a content task whose criterion selects no
session the element holds. A triggering function that reads it reports `triggerFaulty` against
the warrant, rather than at element scope where the attribution would be lost.

## Known gaps

None against the clauses this element implements, once the items under *Deliberate
deviations* are read as decisions. The dispositions above describe the behaviour as it
stands.

**Deliberate divergences, which are not gaps**, are recorded above with their reasoning rather
than repeated here: those under *Deliberate deviations*, the echo substitutions, the timestamp
rendering, the report throttle, and the capabilities declared permanently unsupported in
`li/CONFORMANCE.md`.
