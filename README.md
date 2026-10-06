# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, handles failed tolerance decisions, and seals traceable
evidence records.

Tolerance judgments follow versioned measurement regulations: the steward
publishes regulation versions per device model with effective boundaries, each
case pins the version it was opened under, and later publications only affect
newly opened cases. In-progress cases flagged by a revision need an explicit
steward decision before they can be sealed; sealed evidence is never
rewritten.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow regulation-versioning
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add the unit and integration test suite and replace
or extend these smoke checks with red/green verification tests.

## API

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

## Regulation Versioning

A regulation version is the judgment rule for one device model: a tolerance
with an effective boundary `[effective_from, effective_to)`.

- `POST /regulations` publishes the next version for a model. It closes the
  previous version's effective boundary and, in the same atomic commit, flags
  every unfinished case of that model as `superseded` (listed in the
  response's `superseded_case_ids`). Sealed cases are never touched.
- `POST /cases` no longer takes a tolerance. The case pins the regulation
  version effective for the device's model at open time and inherits its
  tolerance; without an effective version the request fails with
  `regulation_missing`.
- `POST /cases/{id}/regulation` records the steward's decision for a
  `superseded` case: `keep_pinned` finishes it under the pinned version
  (`grandfathered`), `adopt_latest` re-baselines it onto the latest version.
  A `superseded` case cannot be sealed (`regulation_resolution_required`).
- `GET /cases/{id}` embeds the pinned regulation version, and measurements,
  seals, and audit events carry `regulation_version_id`, so the detail and
  event reads restore the basis of every judgment.

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
