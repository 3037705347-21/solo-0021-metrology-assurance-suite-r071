package contracts

import (
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
)

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

type PublishRegulationRequest struct {
	DeviceModel string  `json:"device_model"`
	Revision    string  `json:"revision"`
	Nominal     float64 `json:"nominal"`
	Tolerance   float64 `json:"tolerance"`
	IssuedBy    string  `json:"issued_by"`
}

type OpenCaseRequest struct {
	DeviceID string `json:"device_id"`
	Steward  string `json:"steward"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

type ReopenCaseRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

// RegulationView presents one regulation version with its derived effective
// window. EffectiveTo is empty while the version is the current one for its
// device model.
type RegulationView struct {
	domain.RegulationVersion
	EffectiveTo *time.Time `json:"effective_to,omitempty"`
	Current     bool       `json:"current"`
}

type RegulationResponse struct {
	Regulation RegulationView `json:"regulation"`
}

// PublishRegulationResponse additionally reports which in-progress cases
// were flagged as superseded by the publication.
type PublishRegulationResponse struct {
	Regulation      RegulationView `json:"regulation"`
	SupersededCases []string       `json:"superseded_cases"`
}

type RegulationsResponse struct {
	Regulations []RegulationView `json:"regulations"`
}

type CaseResponse struct {
	Case         domain.VerificationCase `json:"case"`
	Regulation   *RegulationView         `json:"regulation,omitempty"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
