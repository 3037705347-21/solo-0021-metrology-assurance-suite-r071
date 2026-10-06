# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, maintains versioned judgment
regulations per device model, opens verification cases pinned to the
regulation version in effect, records measurements, handles failed tolerance
decisions, and seals traceable evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow publish-regulation
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow regulation-supersede
go run ./cmd/workflowcheck --workflow seal-verification
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
- `GET /regulations` (optional `?model=` filter)
- `GET /regulations/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

### Regulation Versioning

The steward publishes immutable regulation versions per device model. Each
version carries a revision label, nominal value, tolerance, and an
`effective_from` boundary; its window closes when the next version for the
same model takes effect (`effective_to` in read views).

- A case opened afterwards pins the version in effect at that moment and
  snapshots its nominal and tolerance. The pin never changes.
- Publishing a new version only governs cases opened after its boundary.
  Unfinished cases of the same model are flagged with
  `superseded_by_regulation_id`, receive a `regulation_superseded` audit
  event in the same commit, and keep being judged under their pinned version.
- Sealed cases and their evidence seals are never modified retroactively.
- Case detail and event reads embed the pinned regulation and name the
  revision in every judgment event, so the acceptance basis is always
  reconstructable.

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
