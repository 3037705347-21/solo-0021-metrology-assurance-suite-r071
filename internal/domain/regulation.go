package domain

import (
	"fmt"
	"strings"
	"time"
)

// RegulationStatus describes how a case's pinned regulation version relates
// to the latest published version for its device model.
type RegulationStatus string

const (
	// RegulationCurrent means the pinned version is the latest published one.
	RegulationCurrent RegulationStatus = "current"
	// RegulationSuperseded means a newer version was published while the case
	// is unfinished; the case needs an explicit regulation decision.
	RegulationSuperseded RegulationStatus = "superseded"
	// RegulationGrandfathered means a steward explicitly confirmed that the
	// case continues under its pinned (older) version.
	RegulationGrandfathered RegulationStatus = "grandfathered"
)

// RegulationVersion is one published judgment rule for a device model. Its
// content is immutable; only the effective end boundary is written once when
// a newer version supersedes it.
type RegulationVersion struct {
	ID            string     `json:"id"`
	DeviceModel   string     `json:"device_model"`
	Version       int        `json:"version"`
	Tolerance     float64    `json:"tolerance"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	PublishedBy   string     `json:"published_by"`
	PublishedAt   time.Time  `json:"published_at"`
}

func NewRegulationVersion(id, deviceModel string, version int, tolerance float64, effectiveFrom time.Time, publishedBy string, publishedAt time.Time) (RegulationVersion, error) {
	item := RegulationVersion{
		ID:            strings.TrimSpace(id),
		DeviceModel:   strings.TrimSpace(deviceModel),
		Version:       version,
		Tolerance:     tolerance,
		EffectiveFrom: effectiveFrom.UTC(),
		PublishedBy:   strings.TrimSpace(publishedBy),
		PublishedAt:   publishedAt.UTC(),
	}
	if err := item.Validate(); err != nil {
		return RegulationVersion{}, err
	}
	return item, nil
}

func (r RegulationVersion) Validate() error {
	if r.ID == "" || r.DeviceModel == "" || r.PublishedBy == "" {
		return fmt.Errorf("%w: regulation id, device model, and publisher are required", ErrInvalidInput)
	}
	if r.Version < 1 {
		return fmt.Errorf("%w: regulation version must be positive", ErrInvalidInput)
	}
	if !isFinite(r.Tolerance) || r.Tolerance <= 0 {
		return fmt.Errorf("%w: regulation tolerance must be positive and finite", ErrInvalidInput)
	}
	if r.EffectiveFrom.IsZero() || r.PublishedAt.IsZero() {
		return fmt.Errorf("%w: regulation effective and publish times are required", ErrInvalidInput)
	}
	if r.EffectiveTo != nil && !r.EffectiveTo.After(r.EffectiveFrom) {
		return fmt.Errorf("%w: regulation effective boundary must end after it starts", ErrInvalidInput)
	}
	return nil
}

// IsEffectiveAt reports whether the version's effective boundary covers the
// given time: [EffectiveFrom, EffectiveTo), with a nil EffectiveTo meaning
// the version is still the effective one.
func (r RegulationVersion) IsEffectiveAt(at time.Time) bool {
	if at.UTC().Before(r.EffectiveFrom) {
		return false
	}
	return r.EffectiveTo == nil || at.UTC().Before(*r.EffectiveTo)
}

// Supersede closes the effective boundary of the version. It is the only
// permitted mutation of a published regulation version.
func (r *RegulationVersion) Supersede(effectiveTo time.Time) error {
	if r.EffectiveTo != nil {
		return fmt.Errorf("%w: regulation version %s is already superseded", ErrStateConflict, r.ID)
	}
	stamp := effectiveTo.UTC()
	r.EffectiveTo = &stamp
	return nil
}

func NormalizeDeviceModel(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func SameDeviceModel(a, b string) bool {
	return NormalizeDeviceModel(a) == NormalizeDeviceModel(b)
}
