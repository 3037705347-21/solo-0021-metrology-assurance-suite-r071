package domain

import (
	"fmt"
	"strings"
	"time"
)

// EvidenceSeal is the immutable closing record for a reviewed case. It keeps
// the regulation version the case was judged under so sealed evidence always
// explains its own acceptance basis.
type EvidenceSeal struct {
	ID                  string    `json:"id"`
	CaseID              string    `json:"case_id"`
	DeviceID            string    `json:"device_id"`
	MeasurementID       string    `json:"measurement_id"`
	RegulationVersionID string    `json:"regulation_version_id"`
	Reviewer            string    `json:"reviewer"`
	Summary             string    `json:"summary"`
	SealedAt            time.Time `json:"sealed_at"`
}

func NewEvidenceSeal(id, caseID, deviceID, measurementID, regulationVersionID, reviewer, summary string, at time.Time) (EvidenceSeal, error) {
	item := EvidenceSeal{
		ID:                  strings.TrimSpace(id),
		CaseID:              strings.TrimSpace(caseID),
		DeviceID:            strings.TrimSpace(deviceID),
		MeasurementID:       strings.TrimSpace(measurementID),
		RegulationVersionID: strings.TrimSpace(regulationVersionID),
		Reviewer:            strings.TrimSpace(reviewer),
		Summary:             strings.TrimSpace(summary),
		SealedAt:            at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return EvidenceSeal{}, err
	}
	return item, nil
}

func (s EvidenceSeal) Validate() error {
	if s.ID == "" || s.CaseID == "" || s.DeviceID == "" || s.MeasurementID == "" || s.RegulationVersionID == "" || s.Reviewer == "" || s.Summary == "" {
		return fmt.Errorf("%w: seal id, case, device, measurement, regulation version, reviewer, and summary are required", ErrInvalidInput)
	}
	if s.SealedAt.IsZero() {
		return fmt.Errorf("%w: seal time is required", ErrInvalidInput)
	}
	return nil
}
