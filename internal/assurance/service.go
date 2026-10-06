package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

// PublishRegulation appends the next regulation version for a device model.
// The previous version's effective boundary closes at the new version's
// effective start, and every unfinished case of that model that is still
// aligned with an older version is flagged superseded in the same atomic
// commit. Sealed cases are evidence and are never rewritten.
func (s *Service) PublishRegulation(ctx context.Context, input contracts.PublishRegulationRequest) (contracts.PublishRegulationResponse, error) {
	var result contracts.PublishRegulationResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		model := strings.TrimSpace(input.DeviceModel)
		if model == "" {
			return fmt.Errorf("%w: device model is required", domain.ErrInvalidInput)
		}
		effectiveFrom := s.now()
		if input.EffectiveFrom != nil {
			effectiveFrom = input.EffectiveFrom.UTC()
		}
		version := 1
		if latest, exists := m.LatestRegulationForModel(model); exists {
			if !effectiveFrom.After(latest.EffectiveFrom) {
				return fmt.Errorf("%w: effective_from must be later than the effective start of regulation %s", domain.ErrInvalidInput, latest.ID)
			}
			version = latest.Version + 1
			superseded := latest
			if err := superseded.Supersede(effectiveFrom); err != nil {
				return err
			}
			if err := m.UpdateRegulation(superseded); err != nil {
				return err
			}
		}
		item, err := domain.NewRegulationVersion(
			m.NextID("regulation"),
			model,
			version,
			input.Tolerance,
			effectiveFrom,
			input.PublishedBy,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutRegulation(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"regulation",
			item.ID,
			"",
			"regulation_published",
			item.PublishedBy,
			fmt.Sprintf("regulation version %d published for device model %s with tolerance %g", item.Version, item.DeviceModel, item.Tolerance),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event.WithRegulation(item.ID))
		supersededCases := []string{}
		for _, open := range m.UnfinishedCasesForModel(model) {
			if open.RegulationVersionID == item.ID || open.RegulationStatus != domain.RegulationCurrent {
				continue
			}
			if err := open.MarkRegulationSuperseded(s.now()); err != nil {
				return err
			}
			if err := m.UpdateCase(open); err != nil {
				return err
			}
			caseEvent, err := domain.NewAuditEvent(
				m.NextID("event"),
				"case",
				open.ID,
				open.ID,
				"regulation_superseded",
				item.PublishedBy,
				fmt.Sprintf("regulation %s was published; case remains pinned to %s and requires a regulation decision", item.ID, open.RegulationVersionID),
				s.now(),
			)
			if err != nil {
				return err
			}
			m.AppendEvent(caseEvent.WithRegulation(open.RegulationVersionID))
			supersededCases = append(supersededCases, open.ID)
		}
		result = contracts.PublishRegulationResponse{
			Regulation:        item,
			SupersededCaseIDs: supersededCases,
		}
		return nil
	})
	return result, err
}

func (s *Service) Regulation(ctx context.Context, id string) (domain.RegulationVersion, error) {
	var found domain.RegulationVersion
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Regulation(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: regulation %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) Regulations(ctx context.Context, deviceModel string) ([]domain.RegulationVersion, error) {
	result := []domain.RegulationVersion{}
	model := strings.TrimSpace(deviceModel)
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		for _, item := range snapshot.Regulations() {
			if model != "" && !domain.SameDeviceModel(item.DeviceModel, model) {
				continue
			}
			result = append(result, item)
		}
		return nil
	})
	return result, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		regulation, exists := m.RegulationForModelAt(device.Model, s.now())
		if !exists {
			return fmt.Errorf("%w: device model %s has no effective regulation version", domain.ErrRegulationNotFound, device.Model)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			regulation.Tolerance,
			input.Steward,
			regulation.ID,
			s.now(),
		)
		if err != nil {
			return err
		}
		// A newer version that is published but not yet effective still makes
		// the pinned basis outdated, so the case opens needing a decision.
		if latest, ok := m.LatestRegulationForModel(device.Model); ok && latest.ID != regulation.ID {
			item.RegulationStatus = domain.RegulationSuperseded
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			fmt.Sprintf("verification case opened under regulation %s (tolerance %g)", regulation.ID, regulation.Tolerance),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event.WithRegulation(regulation.ID))
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			item.RegulationVersionID,
			input.Technician,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, s.now()); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = "measurement_accepted"
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event.WithRegulation(measurement.RegulationVersionID))
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

// ResolveRegulation records the explicit steward decision for a case whose
// pinned regulation version was superseded: keep the pinned version
// (grandfathered) or adopt the latest published version for the device model.
func (s *Service) ResolveRegulation(ctx context.Context, caseID string, input contracts.ResolveRegulationRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		var detail string
		switch strings.TrimSpace(input.Decision) {
		case contracts.DecisionKeepPinned:
			if err := item.ResolveRegulationKeep(s.now()); err != nil {
				return err
			}
			detail = fmt.Sprintf("keep pinned regulation %s: %s", item.RegulationVersionID, reason)
		case contracts.DecisionAdoptLatest:
			device, ok := m.Device(item.DeviceID)
			if !ok {
				return fmt.Errorf("%w: device %s", domain.ErrNotFound, item.DeviceID)
			}
			latest, ok := m.LatestRegulationForModel(device.Model)
			if !ok {
				return fmt.Errorf("%w: device model %s has no regulation version", domain.ErrRegulationNotFound, device.Model)
			}
			if err := item.ResolveRegulationAdopt(latest.ID, latest.Tolerance, s.now()); err != nil {
				return err
			}
			detail = fmt.Sprintf("adopt regulation %s: %s", latest.ID, reason)
		default:
			return fmt.Errorf("%w: decision must be %s or %s", domain.ErrInvalidInput, contracts.DecisionKeepPinned, contracts.DecisionAdoptLatest)
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"regulation_decision",
			actor,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event.WithRegulation(item.RegulationVersionID))
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			item.RegulationVersionID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event.WithRegulation(seal.RegulationVersionID))
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if regulation, exists := m.Regulation(item.RegulationVersionID); exists {
		result.Regulation = &regulation
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if regulation, exists := snapshot.Regulation(item.RegulationVersionID); exists {
		result.Regulation = &regulation
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
