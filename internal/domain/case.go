package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type CaseState string

const (
	CaseAwaitingMeasurement CaseState = "awaiting_measurement"
	CaseReviewReady         CaseState = "review_ready"
	CaseRecheckRequired     CaseState = "recheck_required"
	CaseSealed              CaseState = "sealed"
)

type VerificationCase struct {
	ID                  string           `json:"id"`
	DeviceID            string           `json:"device_id"`
	Nominal             float64          `json:"nominal"`
	Tolerance           float64          `json:"tolerance"`
	RegulationVersionID string           `json:"regulation_version_id"`
	RegulationStatus    RegulationStatus `json:"regulation_status"`
	Steward             string           `json:"steward"`
	State               CaseState        `json:"state"`
	Version             int              `json:"version"`
	OpenedAt            time.Time        `json:"opened_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	SealedAt            *time.Time       `json:"sealed_at,omitempty"`
}

func NewVerificationCase(id, deviceID string, nominal, tolerance float64, steward, regulationVersionID string, at time.Time) (VerificationCase, error) {
	item := VerificationCase{
		ID:                  strings.TrimSpace(id),
		DeviceID:            strings.TrimSpace(deviceID),
		Nominal:             nominal,
		Tolerance:           tolerance,
		RegulationVersionID: strings.TrimSpace(regulationVersionID),
		RegulationStatus:    RegulationCurrent,
		Steward:             strings.TrimSpace(steward),
		State:               CaseAwaitingMeasurement,
		Version:             1,
		OpenedAt:            at.UTC(),
		UpdatedAt:           at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return VerificationCase{}, err
	}
	return item, nil
}

func (c VerificationCase) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Steward == "" {
		return fmt.Errorf("%w: case id, device id, and steward are required", ErrInvalidInput)
	}
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance <= 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	if c.RegulationVersionID == "" {
		return fmt.Errorf("%w: case regulation version is required", ErrInvalidInput)
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.RegulationStatus {
	case RegulationCurrent, RegulationSuperseded, RegulationGrandfathered:
	default:
		return fmt.Errorf("%w: unsupported regulation status %q", ErrInvalidInput, c.RegulationStatus)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed:
		return nil
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed
}

func (c *VerificationCase) RecordMeasurement(accepted bool, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: case is not awaiting measurement", ErrStateConflict)
	}
	if accepted {
		c.State = CaseReviewReady
	} else {
		c.State = CaseRecheckRequired
	}
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Reopen(at time.Time) error {
	if c.State != CaseRecheckRequired {
		return fmt.Errorf("%w: only a recheck_required case can be reopened", ErrStateConflict)
	}
	c.State = CaseAwaitingMeasurement
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Seal(at time.Time) error {
	if c.State != CaseReviewReady {
		return fmt.Errorf("%w: case is not ready for review", ErrStateConflict)
	}
	if c.RegulationStatus == RegulationSuperseded {
		return fmt.Errorf("%w: case %s requires a regulation decision before sealing", ErrRegulationResolutionRequired, c.ID)
	}
	stamp := at.UTC()
	c.State = CaseSealed
	c.Version++
	c.UpdatedAt = stamp
	c.SealedAt = &stamp
	return nil
}

// MarkRegulationSuperseded flags an unfinished case when a newer regulation
// version is published for its device model. Sealed cases are terminal
// evidence and can never be retroactively rewritten.
func (c *VerificationCase) MarkRegulationSuperseded(at time.Time) error {
	if c.State == CaseSealed {
		return fmt.Errorf("%w: sealed case %s cannot be rewritten by a regulation change", ErrStateConflict, c.ID)
	}
	if c.RegulationStatus != RegulationCurrent {
		return fmt.Errorf("%w: case %s regulation status is %s", ErrStateConflict, c.ID, c.RegulationStatus)
	}
	c.RegulationStatus = RegulationSuperseded
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

// ResolveRegulationKeep is the explicit steward decision to finish the case
// under its pinned (older) regulation version.
func (c *VerificationCase) ResolveRegulationKeep(at time.Time) error {
	if c.State == CaseSealed {
		return fmt.Errorf("%w: sealed case %s cannot change regulation basis", ErrStateConflict, c.ID)
	}
	if c.RegulationStatus != RegulationSuperseded {
		return fmt.Errorf("%w: case %s does not require a regulation decision", ErrStateConflict, c.ID)
	}
	c.RegulationStatus = RegulationGrandfathered
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

// ResolveRegulationAdopt is the explicit steward decision to re-baseline the
// case onto a newer regulation version. Already recorded measurements keep
// the version they were judged under; later measurements use the new one.
func (c *VerificationCase) ResolveRegulationAdopt(regulationVersionID string, tolerance float64, at time.Time) error {
	if c.State == CaseSealed {
		return fmt.Errorf("%w: sealed case %s cannot change regulation basis", ErrStateConflict, c.ID)
	}
	if c.RegulationStatus != RegulationSuperseded {
		return fmt.Errorf("%w: case %s does not require a regulation decision", ErrStateConflict, c.ID)
	}
	if strings.TrimSpace(regulationVersionID) == "" || !isFinite(tolerance) || tolerance <= 0 {
		return fmt.Errorf("%w: adopted regulation version and positive tolerance are required", ErrInvalidInput)
	}
	c.RegulationVersionID = strings.TrimSpace(regulationVersionID)
	c.Tolerance = tolerance
	c.RegulationStatus = RegulationCurrent
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
