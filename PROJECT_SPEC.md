# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, requests a
repeat measurement when tolerance is exceeded, and seals the final evidence
record after review.

Tolerance judgments follow versioned measurement regulations. A steward
publishes regulation versions per device model with explicit effective
boundaries, every case pins the version it was opened under, and later
publications never rewrite in-progress or sealed work silently.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices, publishes regulation versions, opens
  verification cases, and decides how a revised regulation applies to
  in-progress cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- RegulationVersion: one published judgment rule for a device model, with a
  version number, tolerance, effective boundary `[effective_from,
  effective_to)`, publisher, and publish time. Content is immutable; only the
  effective end boundary is written once when a newer version supersedes it.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, the tolerance and regulation version pinned at open time, a regulation
  status, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision, the
  regulation version it was judged under, technician, and timestamp.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement and the regulation version that governed it.
- AuditEvent: an append-only trace of meaningful case, device, and regulation
  changes, carrying the applicable regulation version where one exists.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Publish Regulation Version

The steward sends `POST /regulations` with a device model, tolerance, and
publisher, plus an optional `effective_from` (default: publish time). The new
version must start after the previous version's effective start; publishing
closes the previous version's effective boundary at the new start. In the same
atomic commit, every unfinished case of that device model still aligned with
an older version is flagged `superseded` and receives a
`regulation_superseded` audit event. Sealed cases are never touched. The
response lists the affected case IDs.

### 3. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, and owner.
The service verifies that the device exists, is active, and has no unfinished
case, then resolves the regulation version effective for the device's model at
open time. Without one the request fails with `regulation_missing`. The case
pins that version and its tolerance; if a newer version is already published
but not yet effective, the case opens as `superseded`. A valid request creates
a case in `awaiting_measurement`.

### 4. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service computes the signed deviation from the
nominal value and judges it against the case's pinned tolerance, recording the
pinned regulation version on the measurement. An in-tolerance measurement
moves the case to `review_ready`; an out-of-tolerance measurement moves it to
`recheck_required`.

### 5. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail.

### 6. Resolve Case Regulation

The steward sends `POST /cases/{id}/regulation` with a decision, actor, and
reason for a `superseded` case. `keep_pinned` continues the case under its
pinned version (`grandfathered`); `adopt_latest` re-baselines the case onto
the latest published version and its tolerance (`current`). Already recorded
measurements keep the version they were judged under. The decision is recorded
as a `regulation_decision` audit event.

### 7. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed, and a `superseded` case must first receive
a regulation decision. The service selects the accepted measurement with the
smallest absolute deviation, creates exactly one evidence seal carrying the
case's regulation version, moves the case to `sealed`, and rejects later
mutations.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.

Regulation status of a case:

- `current -> superseded` when a newer version is published for the model
- `superseded -> grandfathered` via a `keep_pinned` decision
- `superseded -> current` via an `adopt_latest` decision (re-pins the case)

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Regulation versions of one device model form a chain of non-overlapping
  effective boundaries in publish order; version content is immutable.
- A case pins exactly one regulation version; its tolerance is the pinned
  version's tolerance at open time or after an `adopt_latest` decision.
- Later regulation publications never change sealed cases, seals, or recorded
  measurements.
- A `superseded` case cannot be sealed until a regulation decision is made.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, change regulation basis,
  or seal again.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /regulations`
- `GET /regulations`
- `GET /regulations/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/regulation`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

The case detail view embeds the pinned regulation version, and every
measurement, seal, and regulation-related audit event carries its
`regulation_version_id`, so both reads restore the judgment basis that applied
at the time.

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal. The regulation-versioning check walks a full revision cycle: publish,
pin, supersede, block a seal, decide, adopt, and confirm that sealed evidence
and its timeline stay pinned to the original version.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
