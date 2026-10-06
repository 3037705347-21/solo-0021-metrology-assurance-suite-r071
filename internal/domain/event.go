package domain

import (
	"fmt"
	"strings"
	"time"
)

type AuditEvent struct {
	ID                  string    `json:"id"`
	SubjectType         string    `json:"subject_type"`
	SubjectID           string    `json:"subject_id"`
	CaseID              string    `json:"case_id,omitempty"`
	Kind                string    `json:"kind"`
	Actor               string    `json:"actor"`
	Detail              string    `json:"detail"`
	RegulationVersionID string    `json:"regulation_version_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

func NewAuditEvent(id, subjectType, subjectID, caseID, kind, actor, detail string, at time.Time) (AuditEvent, error) {
	item := AuditEvent{
		ID:          strings.TrimSpace(id),
		SubjectType: strings.TrimSpace(subjectType),
		SubjectID:   strings.TrimSpace(subjectID),
		CaseID:      strings.TrimSpace(caseID),
		Kind:        strings.TrimSpace(kind),
		Actor:       strings.TrimSpace(actor),
		Detail:      strings.TrimSpace(detail),
		CreatedAt:   at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return AuditEvent{}, err
	}
	return item, nil
}

func (e AuditEvent) Validate() error {
	if e.ID == "" || e.SubjectType == "" || e.SubjectID == "" || e.Kind == "" || e.Actor == "" || e.Detail == "" {
		return fmt.Errorf("%w: audit event fields are required", ErrInvalidInput)
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("%w: event time is required", ErrInvalidInput)
	}
	return nil
}

// WithRegulation attaches the regulation version that the event's subject was
// judged against, so the timeline can restore the basis of each decision.
func (e AuditEvent) WithRegulation(regulationVersionID string) AuditEvent {
	e.RegulationVersionID = strings.TrimSpace(regulationVersionID)
	return e
}
