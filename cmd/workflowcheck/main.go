package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"publish-regulation":       checkPublishRegulation,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"regulation-supersede":     checkRegulationSupersede,
		"seal-verification":        checkSealVerification,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkPublishRegulation() error {
	h := newHarness()
	defer h.close()

	firstID, err := h.mustPublish("Bench Meter", "R1", 25.0, 0.5)
	if err != nil {
		return err
	}

	duplicate, err := h.publishRegulation("Bench Meter", "r1", 25.0, 0.4)
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(duplicate, "duplicate_regulation_revision"); err != nil {
		return err
	}

	invalid, err := h.publishRegulation("Bench Meter", "R2", 25.0, 0)
	if err != nil {
		return err
	}
	if err := expectStatus(invalid, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(invalid, "invalid_input"); err != nil {
		return err
	}

	second, err := h.publishRegulation("Bench Meter", "R2", 25.0, 0.2)
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusCreated); err != nil {
		return err
	}
	secondID, err := nestedString(second.Body, "regulation", "id")
	if err != nil {
		return err
	}
	current, err := nestedBool(second.Body, "regulation", "current")
	if err != nil {
		return err
	}
	if !current {
		return errors.New("newest regulation is not marked current")
	}
	superseded, err := nestedSlice(second.Body, "superseded_cases")
	if err != nil {
		return err
	}
	if len(superseded) != 0 {
		return fmt.Errorf("no cases exist yet, superseded_cases = %v", superseded)
	}

	listed, err := h.request(http.MethodGet, "/regulations?model="+url.QueryEscape("bench meter"), nil)
	if err != nil {
		return err
	}
	if err := expectStatus(listed, http.StatusOK); err != nil {
		return err
	}
	entries, err := nestedSlice(listed.Body, "regulations")
	if err != nil {
		return err
	}
	if len(entries) != 2 {
		return fmt.Errorf("expected 2 regulation versions, got %d", len(entries))
	}
	firstFrom, err := nestedString(listed.Body, "regulations", 0, "effective_from")
	if err != nil {
		return err
	}
	firstTo, err := nestedString(listed.Body, "regulations", 0, "effective_to")
	if err != nil {
		return err
	}
	secondFrom, err := nestedString(listed.Body, "regulations", 1, "effective_from")
	if err != nil {
		return err
	}
	if firstTo != secondFrom {
		return fmt.Errorf("first window closes at %q, second opens at %q", firstTo, secondFrom)
	}
	if !firstToAfterOrEqual(firstTo, firstFrom) {
		return fmt.Errorf("effective window %q -> %q is inverted", firstFrom, firstTo)
	}
	if _, err := nestedString(listed.Body, "regulations", 1, "effective_to"); err == nil {
		return errors.New("current regulation must not have effective_to")
	}

	read, err := h.request(http.MethodGet, "/regulations/"+firstID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readRevision, err := nestedString(read.Body, "regulation", "revision")
	if err != nil {
		return err
	}
	if readRevision != "R1" {
		return fmt.Errorf("read returned revision %q, expected R1", readRevision)
	}

	secondRead, err := h.request(http.MethodGet, "/regulations/"+secondID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(secondRead, http.StatusOK); err != nil {
		return err
	}

	missing, err := h.request(http.MethodGet, "/regulations/regulation-9999", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusNotFound); err != nil {
		return err
	}
	return expectErrorCode(missing, "not_found")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	blocked, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "no_effective_regulation"); err != nil {
		return err
	}

	firstRegulation, err := h.mustPublish("Bench Meter", "R1", 25.0, 0.5)
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}
	pinned, err := nestedString(opened.Body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if pinned != firstRegulation {
		return fmt.Errorf("case pinned regulation %q, expected %q", pinned, firstRegulation)
	}
	tolerance, err := nestedFloat(opened.Body, "case", "tolerance")
	if err != nil {
		return err
	}
	if tolerance != 0.5 {
		return fmt.Errorf("case tolerance = %v, expected 0.5 from regulation R1", tolerance)
	}
	basis, err := nestedString(opened.Body, "regulation", "revision")
	if err != nil {
		return err
	}
	if basis != "R1" {
		return fmt.Errorf("case detail regulation revision = %q, expected R1", basis)
	}

	second, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "active_case_exists"); err != nil {
		return err
	}

	// A later regulation release only governs cases opened afterwards.
	secondRegulation, err := h.mustPublish("Bench Meter", "R2", 25.0, 0.1)
	if err != nil {
		return err
	}
	otherDeviceID, err := h.createDevice("MET-1102")
	if err != nil {
		return err
	}
	otherOpened, err := h.openCase(otherDeviceID)
	if err != nil {
		return err
	}
	otherPinned, err := nestedString(otherOpened.Body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if otherPinned != secondRegulation {
		return fmt.Errorf("new case pinned regulation %q, expected %q", otherPinned, secondRegulation)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	stillPinned, err := nestedString(read.Body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if stillPinned != firstRegulation {
		return fmt.Errorf("existing case regulation changed to %q after R2 was published", stillPinned)
	}
	return nil
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	if _, err := h.mustPublish("Bench Meter", "R1", 25.0, 0.5); err != nil {
		return err
	}
	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}
	judgedUnder, err := nestedString(accepted.Body, "measurements", 0, "regulation_version_id")
	if err != nil {
		return err
	}
	pinned, err := nestedString(accepted.Body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if judgedUnder != pinned {
		return fmt.Errorf("measurement judged under %q, case pinned to %q", judgedUnder, pinned)
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}

	// A case pinned to R1 (tolerance 0.5) keeps its criteria after R2
	// (tolerance 0.1) takes effect: 25.3 is accepted under R1 only.
	pinnedDeviceID, err := h.createDevice("MET-1203")
	if err != nil {
		return err
	}
	pinnedCase, err := h.openCase(pinnedDeviceID)
	if err != nil {
		return err
	}
	pinnedCaseID, err := nestedString(pinnedCase.Body, "case", "id")
	if err != nil {
		return err
	}
	if _, err := h.mustPublish("Bench Meter", "R2", 25.0, 0.1); err != nil {
		return err
	}
	judged, err := h.recordMeasurement(pinnedCaseID, 25.3, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(judged, http.StatusCreated); err != nil {
		return err
	}
	judgedAccepted, err := nestedBool(judged.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !judgedAccepted {
		return errors.New("case was re-judged under the newer regulation instead of its pinned version")
	}
	judgedState, err := nestedString(judged.Body, "case", "state")
	if err != nil {
		return err
	}
	if judgedState != "review_ready" {
		return fmt.Errorf("pinned case state = %q, expected review_ready", judgedState)
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	if _, err := h.mustPublish("Bench Meter", "R1", 25.0, 0.5); err != nil {
		return err
	}
	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkRegulationSupersede() error {
	h := newHarness()
	defer h.close()

	firstRegulation, err := h.mustPublish("Bench Meter", "R1", 25.0, 0.5)
	if err != nil {
		return err
	}
	deviceA, err := h.createDevice("MET-1501")
	if err != nil {
		return err
	}
	openedA, err := h.openCase(deviceA)
	if err != nil {
		return err
	}
	caseA, err := nestedString(openedA.Body, "case", "id")
	if err != nil {
		return err
	}

	// Publishing R2 must flag the in-progress case in the same response.
	second, err := h.publishRegulation("Bench Meter", "R2", 25.0, 0.2)
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusCreated); err != nil {
		return err
	}
	secondRegulation, err := nestedString(second.Body, "regulation", "id")
	if err != nil {
		return err
	}
	superseded, err := nestedSlice(second.Body, "superseded_cases")
	if err != nil {
		return err
	}
	if len(superseded) != 1 || superseded[0] != caseA {
		return fmt.Errorf("superseded_cases = %v, expected [%s]", superseded, caseA)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseA, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	flaggedBy, err := nestedString(read.Body, "case", "superseded_by_regulation_id")
	if err != nil {
		return err
	}
	if flaggedBy != secondRegulation {
		return fmt.Errorf("case superseded_by = %q, expected %q", flaggedBy, secondRegulation)
	}
	stillPinned, err := nestedString(read.Body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if stillPinned != firstRegulation {
		return fmt.Errorf("supersede rewrote the pinned regulation to %q", stillPinned)
	}
	basis, err := nestedString(read.Body, "regulation", "revision")
	if err != nil {
		return err
	}
	if basis != "R1" {
		return fmt.Errorf("case detail regulation revision = %q, expected R1", basis)
	}
	events, err := h.request(http.MethodGet, "/cases/"+caseA+"/events", nil)
	if err != nil {
		return err
	}
	if !hasEvent(events.Body, "regulation_superseded") {
		return errors.New("regulation_superseded audit event is missing")
	}

	// A new case pins R2; publishing R3 flags both unfinished cases.
	deviceB, err := h.createDevice("MET-1502")
	if err != nil {
		return err
	}
	openedB, err := h.openCase(deviceB)
	if err != nil {
		return err
	}
	caseB, err := nestedString(openedB.Body, "case", "id")
	if err != nil {
		return err
	}
	pinnedB, err := nestedString(openedB.Body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if pinnedB != secondRegulation {
		return fmt.Errorf("case B pinned %q, expected %q", pinnedB, secondRegulation)
	}
	third, err := h.publishRegulation("Bench Meter", "R3", 25.0, 0.1)
	if err != nil {
		return err
	}
	if err := expectStatus(third, http.StatusCreated); err != nil {
		return err
	}
	thirdRegulation, err := nestedString(third.Body, "regulation", "id")
	if err != nil {
		return err
	}
	supersededAgain, err := nestedSlice(third.Body, "superseded_cases")
	if err != nil {
		return err
	}
	if len(supersededAgain) != 2 {
		return fmt.Errorf("superseded_cases = %v, expected both open cases", supersededAgain)
	}
	readB, err := h.request(http.MethodGet, "/cases/"+caseB, nil)
	if err != nil {
		return err
	}
	flaggedB, err := nestedString(readB.Body, "case", "superseded_by_regulation_id")
	if err != nil {
		return err
	}
	if flaggedB != thirdRegulation {
		return fmt.Errorf("case B superseded_by = %q, expected %q", flaggedB, thirdRegulation)
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	regulationID, err := h.mustPublish("Bench Meter", "R1", 25.0, 0.5)
	if err != nil {
		return err
	}
	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	sealRegulation, err := nestedString(read.Body, "seal", "regulation_version_id")
	if err != nil {
		return err
	}
	if sealRegulation != regulationID {
		return fmt.Errorf("seal recorded regulation %q, expected %q", sealRegulation, regulationID)
	}

	// Sealed evidence is immutable: a later regulation release must not
	// flag or otherwise rewrite the sealed case.
	published, err := h.publishRegulation("Bench Meter", "R2", 25.0, 0.1)
	if err != nil {
		return err
	}
	if err := expectStatus(published, http.StatusCreated); err != nil {
		return err
	}
	superseded, err := nestedSlice(published.Body, "superseded_cases")
	if err != nil {
		return err
	}
	if len(superseded) != 0 {
		return fmt.Errorf("sealed case was retroactively flagged: %v", superseded)
	}
	reread, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(reread, http.StatusOK); err != nil {
		return err
	}
	if _, err := nestedString(reread.Body, "case", "superseded_by_regulation_id"); err == nil {
		return errors.New("sealed case gained a superseded flag after R2 was published")
	}
	sealedBasis, err := nestedString(reread.Body, "seal", "regulation_version_id")
	if err != nil {
		return err
	}
	if sealedBasis != regulationID {
		return fmt.Errorf("sealed evidence regulation changed to %q", sealedBasis)
	}
	basis, err := nestedString(reread.Body, "regulation", "revision")
	if err != nil {
		return err
	}
	if basis != "R1" {
		return fmt.Errorf("sealed case detail regulation revision = %q, expected R1", basis)
	}

	reopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "attempt to rewrite sealed evidence",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopen, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(reopen, "state_conflict"); err != nil {
		return err
	}
	measure, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(measure, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(measure, "state_conflict")
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) publishRegulation(model, revision string, nominal, tolerance float64) (apiResponse, error) {
	return h.request(http.MethodPost, "/regulations", map[string]any{
		"device_model": model,
		"revision":     revision,
		"nominal":      nominal,
		"tolerance":    tolerance,
		"issued_by":    "Qi",
	})
}

func (h *harness) mustPublish(model, revision string, nominal, tolerance float64) (string, error) {
	response, err := h.publishRegulation(model, revision, nominal, tolerance)
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "regulation", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"steward":   "Qiao",
	})
}

func (h *harness) mustOpenCase(deviceID string) (apiResponse, error) {
	response, err := h.openCase(deviceID)
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.mustOpenCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.mustOpenCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...any) (string, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", path)
	}
	return text, nil
}

func nestedFloat(root map[string]any, path ...any) (float64, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%v is not a number", path)
	}
	return number, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func firstToAfterOrEqual(later, earlier string) bool {
	laterTime, err := time.Parse(time.RFC3339Nano, later)
	if err != nil {
		return false
	}
	earlierTime, err := time.Parse(time.RFC3339Nano, earlier)
	if err != nil {
		return false
	}
	return !laterTime.Before(earlierTime)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
