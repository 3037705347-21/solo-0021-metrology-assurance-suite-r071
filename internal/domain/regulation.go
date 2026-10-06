package domain

import (
	"fmt"
	"strings"
	"time"
)

// RegulationVersion is one immutable release of the tolerance judgment
// regulation for a device model. Its effective window starts at
// EffectiveFrom and ends when the next version for the same model takes
// effect. Versions are never updated or deleted after publication, so a
// case that pinned a version can always reconstruct the rule it used.
type RegulationVersion struct {
	ID            string    `json:"id"`
	DeviceModel   string    `json:"device_model"`
	Revision      string    `json:"revision"`
	Nominal       float64   `json:"nominal"`
	Tolerance     float64   `json:"tolerance"`
	IssuedBy      string    `json:"issued_by"`
	EffectiveFrom time.Time `json:"effective_from"`
}

func NewRegulationVersion(id, deviceModel, revision string, nominal, tolerance float64, issuedBy string, effectiveFrom time.Time) (RegulationVersion, error) {
	item := RegulationVersion{
		ID:            strings.TrimSpace(id),
		DeviceModel:   strings.TrimSpace(deviceModel),
		Revision:      strings.TrimSpace(revision),
		Nominal:       nominal,
		Tolerance:     tolerance,
		IssuedBy:      strings.TrimSpace(issuedBy),
		EffectiveFrom: effectiveFrom.UTC(),
	}
	if err := item.Validate(); err != nil {
		return RegulationVersion{}, err
	}
	return item, nil
}

func (r RegulationVersion) Validate() error {
	if r.ID == "" || r.DeviceModel == "" || r.Revision == "" || r.IssuedBy == "" {
		return fmt.Errorf("%w: regulation id, device model, revision, and issuer are required", ErrInvalidInput)
	}
	if !isFinite(r.Nominal) || !isFinite(r.Tolerance) || r.Tolerance <= 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	if r.EffectiveFrom.IsZero() {
		return fmt.Errorf("%w: regulation effective time is required", ErrInvalidInput)
	}
	return nil
}

// Label returns a human-readable reference used in audit details.
func (r RegulationVersion) Label() string {
	return fmt.Sprintf("%s (%s)", r.Revision, r.ID)
}

// NormalizeModel canonicalizes a device model for regulation matching.
func NormalizeModel(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

// MatchesModel reports whether the version governs the given device model.
func (r RegulationVersion) MatchesModel(deviceModel string) bool {
	return NormalizeModel(r.DeviceModel) == NormalizeModel(deviceModel)
}

// ResolveEffective returns the version whose effective window contains at:
// the version with the latest EffectiveFrom that is not after at.
func ResolveEffective(versions []RegulationVersion, at time.Time) (RegulationVersion, bool) {
	var selected RegulationVersion
	found := false
	stamp := at.UTC()
	for _, item := range versions {
		if item.EffectiveFrom.After(stamp) {
			continue
		}
		if !found || item.EffectiveFrom.After(selected.EffectiveFrom) {
			selected = item
			found = true
		}
	}
	return selected, found
}
