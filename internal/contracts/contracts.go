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

// OpenCaseRequest opens a case under the regulation version that is effective
// for the device's model at open time; the tolerance comes from that version.
type OpenCaseRequest struct {
	DeviceID string  `json:"device_id"`
	Nominal  float64 `json:"nominal"`
	Steward  string  `json:"steward"`
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

// PublishRegulationRequest publishes the next regulation version for a device
// model. EffectiveFrom defaults to the publish time and must be later than
// the previous version's effective start.
type PublishRegulationRequest struct {
	DeviceModel   string     `json:"device_model"`
	Tolerance     float64    `json:"tolerance"`
	PublishedBy   string     `json:"published_by"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
}

// Regulation decisions for an unfinished case whose pinned version was
// superseded by a newer publication.
const (
	DecisionKeepPinned  = "keep_pinned"
	DecisionAdoptLatest = "adopt_latest"
)

type ResolveRegulationRequest struct {
	Decision string `json:"decision"`
	Actor    string `json:"actor"`
	Reason   string `json:"reason"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

type RegulationResponse struct {
	Regulation domain.RegulationVersion `json:"regulation"`
}

type PublishRegulationResponse struct {
	Regulation        domain.RegulationVersion `json:"regulation"`
	SupersededCaseIDs []string                 `json:"superseded_case_ids"`
}

type RegulationsResponse struct {
	Regulations []domain.RegulationVersion `json:"regulations"`
}

type CaseResponse struct {
	Case         domain.VerificationCase   `json:"case"`
	Regulation   *domain.RegulationVersion `json:"regulation,omitempty"`
	Measurements []domain.Measurement      `json:"measurements"`
	Seal         *domain.EvidenceSeal      `json:"seal,omitempty"`
	Events       []domain.AuditEvent       `json:"events"`
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
