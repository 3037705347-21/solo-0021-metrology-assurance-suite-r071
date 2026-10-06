# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, maintains
versioned judgment regulations per device model, opens a verification case
pinned to the regulation version in effect, records technician measurements,
requests a repeat measurement when tolerance is exceeded, and seals the final
evidence record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices, publishes regulation versions, and
  opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- RegulationVersion: one immutable release of the judgment regulation for a
  device model, with a revision label, nominal value, tolerance, issuer, and
  the effective boundary from which it governs. Its effective window closes
  when the next version for the same model takes effect.
- VerificationCase: one bounded assurance activity for a device, pinned to the
  regulation version that was effective when the case opened, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision, the
  regulation version it was judged under, technician, and timestamp.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement and the regulation version that backed it.
- AuditEvent: an append-only trace of meaningful case, device, and regulation
  changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Publish Regulation

The steward sends `POST /regulations` with a device model, revision label,
nominal value, tolerance, and issuer. The version takes effect immediately and
is immutable afterwards. Revision labels are unique per model, and a new
version must take effect after every existing version for that model, so
effective windows never overlap. In the same commit, every unfinished case of
that model is flagged with `superseded_by_regulation_id` and receives a
`regulation_superseded` audit event; the publication response lists those
affected cases. Sealed cases are never modified.

### 3. Open Verification Case

The steward sends `POST /cases` with a device ID and steward. The service
verifies that the device exists, is active, and has no unfinished case, then
resolves the regulation version effective for the device model at that moment.
If no version is in effect, the request is rejected with
`no_effective_regulation`. A valid request creates a case in
`awaiting_measurement` that snapshots the nominal value and tolerance from the
regulation version and pins `regulation_version_id` permanently. Regulation
releases afterwards only affect cases opened after their effective boundary.

### 4. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service computes the signed deviation from the
nominal value and judges it against the case's pinned regulation criteria,
even if a newer regulation version has since taken effect. The measurement
records the regulation version it was judged under. An in-tolerance
measurement moves the case to `review_ready`; an out-of-tolerance measurement
moves it to `recheck_required`.

### 5. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail.

### 6. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, creates exactly one evidence
seal carrying the case's pinned regulation version, moves the case to
`sealed`, and rejects later mutations. A sealed case is never flagged or
rewritten by later regulation releases.

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

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Regulation revisions are unique per device model, take effect in strictly
  increasing order per model, and are immutable once published.
- A case pins exactly one regulation version at open time; the pin and the
  snapshotted nominal and tolerance never change.
- A case cannot be opened unless a regulation version is in effect for the
  device model.
- Publishing a regulation flags every unfinished case of that model as
  superseded and appends a `regulation_superseded` audit event to each in the
  same repository commit; the cases continue under their pinned versions.
- Sealed cases and their evidence seals are never modified, including by
  regulation publication.
- Measurements and evidence seals record the regulation version they were
  judged under.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
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
- `GET /regulations` (optional `?model=` filter)
- `GET /regulations/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

Case detail responses embed the pinned regulation version with its derived
effective window, and case event streams include `case_opened`,
`regulation_superseded`, measurement, and seal entries whose details name the
regulation revision in force, so both reads reconstruct the acceptance basis
that applied at the time.

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The regulation checks prove that a case
keeps judging under its pinned version after a revision, that in-progress
cases are flagged and audited when a revision takes effect, and that sealed
evidence is never rewritten. The seal check also races two concurrent seal
requests to prove that the repository mutation lock permits exactly one seal.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
