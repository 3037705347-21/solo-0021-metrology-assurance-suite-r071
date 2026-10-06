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

// PublishRegulation releases a new immutable regulation version for a device
// model. The version takes effect immediately, so it only governs cases
// opened afterwards. Every unfinished case of the same model that is pinned
// to an older version is flagged as superseded and receives an audit event
// in the same commit; sealed cases are never touched.
func (s *Service) PublishRegulation(ctx context.Context, input contracts.PublishRegulationRequest) (contracts.PublishRegulationResponse, error) {
	var result contracts.PublishRegulationResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		now := s.now()
		existing := m.RegulationsForModel(input.DeviceModel)
		revision := strings.TrimSpace(input.Revision)
		for _, item := range existing {
			if strings.EqualFold(item.Revision, revision) {
				return fmt.Errorf("%w: model %s already has revision %s", domain.ErrDuplicateRegulationRevision, domain.NormalizeModel(input.DeviceModel), item.Revision)
			}
		}
		if latest, ok := latestRegulation(existing); ok && !now.After(latest.EffectiveFrom) {
			return fmt.Errorf("%w: new regulation for model %s must take effect after revision %s", domain.ErrStateConflict, domain.NormalizeModel(input.DeviceModel), latest.Revision)
		}
		item, err := domain.NewRegulationVersion(
			m.NextID("regulation"),
			input.DeviceModel,
			input.Revision,
			input.Nominal,
			input.Tolerance,
			input.IssuedBy,
			now,
		)
		if err != nil {
			return err
		}
		if err := m.PutRegulation(item); err != nil {
			return err
		}
		superseded := make([]string, 0)
		for _, open := range m.UnfinishedCasesForModel(item.DeviceModel) {
			if open.RegulationVersionID == item.ID {
				continue
			}
			pinned, pinnedExists := m.Regulation(open.RegulationVersionID)
			if err := open.MarkRegulationSuperseded(item.ID, now); err != nil {
				return err
			}
			if err := m.UpdateCase(open); err != nil {
				return err
			}
			detail := fmt.Sprintf("regulation revision %s took effect; case continues under pinned revision %s", item.Label(), open.RegulationVersionID)
			if pinnedExists {
				detail = fmt.Sprintf("regulation revision %s took effect; case continues under pinned revision %s", item.Label(), pinned.Label())
			}
			event, err := domain.NewAuditEvent(
				m.NextID("event"),
				"case",
				open.ID,
				open.ID,
				"regulation_superseded",
				item.IssuedBy,
				detail,
				now,
			)
			if err != nil {
				return err
			}
			m.AppendEvent(event)
			superseded = append(superseded, open.ID)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"regulation",
			item.ID,
			"",
			"regulation_published",
			item.IssuedBy,
			fmt.Sprintf("regulation revision %s published for model %s with nominal %g and tolerance %g", item.Label(), domain.NormalizeModel(item.DeviceModel), item.Nominal, item.Tolerance),
			now,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = contracts.PublishRegulationResponse{
			Regulation:      regulationView(m.RegulationsForModel(item.DeviceModel), item),
			SupersededCases: superseded,
		}
		return nil
	})
	return result, err
}

func (s *Service) Regulation(ctx context.Context, id string) (contracts.RegulationResponse, error) {
	var result contracts.RegulationResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Regulation(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: regulation %s", domain.ErrNotFound, id)
		}
		result = contracts.RegulationResponse{
			Regulation: regulationView(snapshot.RegulationsForModel(item.DeviceModel), item),
		}
		return nil
	})
	return result, err
}

func (s *Service) Regulations(ctx context.Context, deviceModel string) (contracts.RegulationsResponse, error) {
	var result contracts.RegulationsResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		var versions []domain.RegulationVersion
		if strings.TrimSpace(deviceModel) == "" {
			versions = snapshot.AllRegulations()
		} else {
			versions = snapshot.RegulationsForModel(deviceModel)
		}
		views := make([]contracts.RegulationView, 0, len(versions))
		for _, item := range versions {
			views = append(views, regulationView(snapshot.RegulationsForModel(item.DeviceModel), item))
		}
		result = contracts.RegulationsResponse{Regulations: views}
		return nil
	})
	return result, err
}

// OpenCase pins the case to the regulation version that is effective for the
// device model at open time. The nominal and tolerance are snapshotted from
// that version, so later regulation releases cannot change how the case is
// judged.
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
		regulation, ok := domain.ResolveEffective(m.RegulationsForModel(device.Model), s.now())
		if !ok {
			return fmt.Errorf("%w: model %s has no regulation in effect", domain.ErrNoEffectiveRegulation, domain.NormalizeModel(device.Model))
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			regulation.Nominal,
			regulation.Tolerance,
			regulation.ID,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
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
			fmt.Sprintf("verification case opened under regulation revision %s", regulation.Label()),
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

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponse(snapshot, item)
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
		detail = fmt.Sprintf("%s under regulation revision %s", detail, regulationLabel(m, item.RegulationVersionID))
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
		m.AppendEvent(event)
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
			fmt.Sprintf("%s; sealed under regulation revision %s", seal.Summary, regulationLabel(m, item.RegulationVersionID)),
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

func latestRegulation(versions []domain.RegulationVersion) (domain.RegulationVersion, bool) {
	var latest domain.RegulationVersion
	found := false
	for _, item := range versions {
		if !found || item.EffectiveFrom.After(latest.EffectiveFrom) {
			latest = item
			found = true
		}
	}
	return latest, found
}

// regulationView derives the effective window of a version from its model's
// version list: the window closes when the next version takes effect.
func regulationView(versions []domain.RegulationVersion, current domain.RegulationVersion) contracts.RegulationView {
	view := contracts.RegulationView{RegulationVersion: current, Current: true}
	for _, candidate := range versions {
		if candidate.ID == current.ID || !candidate.EffectiveFrom.After(current.EffectiveFrom) {
			continue
		}
		if view.EffectiveTo == nil || candidate.EffectiveFrom.Before(*view.EffectiveTo) {
			stamp := candidate.EffectiveFrom
			view.EffectiveTo = &stamp
		}
	}
	view.Current = view.EffectiveTo == nil
	return view
}

func regulationLabel(reader repository.Reader, regulationVersionID string) string {
	if item, exists := reader.Regulation(regulationVersionID); exists {
		return item.Label()
	}
	return regulationVersionID
}

func caseResponse(reader repository.Reader, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: reader.Measurements(item.ID),
		Events:       reader.Events(item.ID),
	}
	if regulation, exists := reader.Regulation(item.RegulationVersionID); exists {
		view := regulationView(reader.RegulationsForModel(regulation.DeviceModel), regulation)
		result.Regulation = &view
	}
	if seal, exists := reader.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
