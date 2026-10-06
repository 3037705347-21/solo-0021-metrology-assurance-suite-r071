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
	"os"
	"strings"
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
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
		"regulation-versioning":    checkRegulationVersioning,
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

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	unregulated, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(unregulated, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(unregulated, "regulation_missing"); err != nil {
		return err
	}
	if _, err := h.publishRegulation("Bench Meter", 0.5); err != nil {
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

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	if _, err := h.publishRegulation("Bench Meter", 0.5); err != nil {
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
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

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

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

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
	return nil
}

func checkRegulationVersioning() error {
	h := newHarness()
	defer h.close()

	deviceA, err := h.createDevice("MET-1501")
	if err != nil {
		return err
	}
	unregulated, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceA,
		"nominal":   25.0,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(unregulated, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(unregulated, "regulation_missing"); err != nil {
		return err
	}

	regV1, err := h.publishRegulation("Bench Meter", 0.5)
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceA)
	if err != nil {
		return err
	}
	case1, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	if err := expectCaseRegulation(opened.Body, regV1, "current", 0.5); err != nil {
		return fmt.Errorf("case1 open: %w", err)
	}
	accepted, err := h.recordMeasurement(case1, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	measurementReg, err := nestedStringAt(accepted.Body, "measurements", 0, "regulation_version_id")
	if err != nil {
		return err
	}
	if measurementReg != regV1 {
		return fmt.Errorf("measurement judged under %q, expected %q", measurementReg, regV1)
	}

	deviceB, err := h.createDevice("MET-1502")
	if err != nil {
		return err
	}
	openedB, err := h.openCase(deviceB)
	if err != nil {
		return err
	}
	case2, err := nestedString(openedB.Body, "case", "id")
	if err != nil {
		return err
	}

	regV2, err := h.publishRegulation("Bench Meter", 0.1)
	if err != nil {
		return err
	}
	pub2, err := h.request(http.MethodGet, "/regulations/"+regV2, nil)
	if err != nil {
		return err
	}
	version2, err := nestedNumber(pub2.Body, "regulation", "version")
	if err != nil {
		return err
	}
	if version2 != 2 {
		return fmt.Errorf("second publication version = %v, expected 2", version2)
	}
	v1View, err := h.request(http.MethodGet, "/regulations/"+regV1, nil)
	if err != nil {
		return err
	}
	if _, err := nestedString(v1View.Body, "regulation", "effective_to"); err != nil {
		return fmt.Errorf("superseded regulation %s has no effective end boundary: %w", regV1, err)
	}

	blocked, err := h.request(http.MethodPost, "/cases/"+case1+"/seal", map[string]any{
		"reviewer": "Vera",
		"summary":  "attempted seal under superseded regulation",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "regulation_resolution_required"); err != nil {
		return err
	}
	view1, err := h.request(http.MethodGet, "/cases/"+case1, nil)
	if err != nil {
		return err
	}
	if err := expectCaseRegulation(view1.Body, regV1, "superseded", 0.5); err != nil {
		return fmt.Errorf("case1 after revision: %w", err)
	}

	kept, err := h.request(http.MethodPost, "/cases/"+case1+"/regulation", map[string]any{
		"decision": "keep_pinned",
		"actor":    "Lin",
		"reason":   "field campaign started under the previous regulation",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(kept, http.StatusOK); err != nil {
		return err
	}
	if err := expectCaseRegulation(kept.Body, regV1, "grandfathered", 0.5); err != nil {
		return fmt.Errorf("case1 keep_pinned: %w", err)
	}
	sealed, err := h.request(http.MethodPost, "/cases/"+case1+"/seal", map[string]any{
		"reviewer": "Vera",
		"summary":  "sealed under the pinned regulation version",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}
	sealReg, err := nestedString(sealed.Body, "seal", "regulation_version_id")
	if err != nil {
		return err
	}
	if sealReg != regV1 {
		return fmt.Errorf("seal regulation = %q, expected pinned %q", sealReg, regV1)
	}
	sealedEvents, err := nestedSlice(sealed.Body, "events")
	if err != nil {
		return err
	}

	adopted, err := h.request(http.MethodPost, "/cases/"+case2+"/regulation", map[string]any{
		"decision": "adopt_latest",
		"actor":    "Lin",
		"reason":   "re-baseline to the current regulation before measuring",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(adopted, http.StatusOK); err != nil {
		return err
	}
	if err := expectCaseRegulation(adopted.Body, regV2, "current", 0.1); err != nil {
		return fmt.Errorf("case2 adopt_latest: %w", err)
	}
	rejected, err := h.recordMeasurement(case2, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("measurement within the old tolerance was accepted under the adopted regulation")
	}
	rejectedReg, err := nestedStringAt(rejected.Body, "measurements", 0, "regulation_version_id")
	if err != nil {
		return err
	}
	if rejectedReg != regV2 {
		return fmt.Errorf("adopted measurement judged under %q, expected %q", rejectedReg, regV2)
	}

	pub3, err := h.request(http.MethodPost, "/regulations", map[string]any{
		"device_model": "Bench Meter",
		"tolerance":    0.05,
		"published_by": "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(pub3, http.StatusCreated); err != nil {
		return err
	}
	regV3, err := nestedString(pub3.Body, "regulation", "id")
	if err != nil {
		return err
	}
	affected, err := nestedSlice(pub3.Body, "superseded_case_ids")
	if err != nil {
		return err
	}
	if len(affected) != 1 || affected[0] != case2 {
		return fmt.Errorf("third publication superseded %v, expected only %s", affected, case2)
	}

	sealedView, err := h.request(http.MethodGet, "/cases/"+case1, nil)
	if err != nil {
		return err
	}
	if err := expectCaseRegulation(sealedView.Body, regV1, "grandfathered", 0.5); err != nil {
		return fmt.Errorf("sealed case1 after third publication: %w", err)
	}
	sealedState, err := nestedString(sealedView.Body, "case", "state")
	if err != nil {
		return err
	}
	if sealedState != "sealed" {
		return fmt.Errorf("sealed case state = %q", sealedState)
	}
	sealedViewEvents, err := nestedSlice(sealedView.Body, "events")
	if err != nil {
		return err
	}
	if len(sealedViewEvents) != len(sealedEvents) {
		return fmt.Errorf("sealed case gained %d events after the third publication", len(sealedViewEvents)-len(sealedEvents))
	}

	timeline, err := h.request(http.MethodGet, "/cases/"+case1+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(timeline, http.StatusOK); err != nil {
		return err
	}
	for _, kind := range []string{"case_opened", "regulation_superseded", "regulation_decision", "case_sealed"} {
		event := findEvent(timeline.Body, kind)
		if event == nil {
			return fmt.Errorf("sealed case timeline is missing %s", kind)
		}
		if event["regulation_version_id"] != regV1 {
			return fmt.Errorf("event %s regulation = %v, expected %s", kind, event["regulation_version_id"], regV1)
		}
	}

	deviceC, err := h.createDevice("MET-1503")
	if err != nil {
		return err
	}
	openedC, err := h.openCase(deviceC)
	if err != nil {
		return err
	}
	if err := expectCaseRegulation(openedC.Body, regV3, "current", 0.05); err != nil {
		return fmt.Errorf("case3 open: %w", err)
	}
	case3, err := nestedString(openedC.Body, "case", "id")
	if err != nil {
		return err
	}

	invalid, err := h.request(http.MethodPost, "/cases/"+case2+"/regulation", map[string]any{
		"decision": "whatever",
		"actor":    "Lin",
		"reason":   "not a real decision",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(invalid, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(invalid, "invalid_input"); err != nil {
		return err
	}
	unneeded, err := h.request(http.MethodPost, "/cases/"+case3+"/regulation", map[string]any{
		"decision": "keep_pinned",
		"actor":    "Lin",
		"reason":   "case is already on the latest regulation",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(unneeded, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(unneeded, "state_conflict")
}

func expectCaseRegulation(body map[string]any, regulationID, status string, tolerance float64) error {
	pinned, err := nestedString(body, "case", "regulation_version_id")
	if err != nil {
		return err
	}
	if pinned != regulationID {
		return fmt.Errorf("pinned regulation = %q, expected %q", pinned, regulationID)
	}
	actualStatus, err := nestedString(body, "case", "regulation_status")
	if err != nil {
		return err
	}
	if actualStatus != status {
		return fmt.Errorf("regulation status = %q, expected %q", actualStatus, status)
	}
	actualTolerance, err := nestedNumber(body, "case", "tolerance")
	if err != nil {
		return err
	}
	if actualTolerance != tolerance {
		return fmt.Errorf("tolerance = %v, expected %v", actualTolerance, tolerance)
	}
	embedded, err := nestedString(body, "regulation", "id")
	if err != nil {
		return err
	}
	if embedded != regulationID {
		return fmt.Errorf("embedded regulation = %q, expected %q", embedded, regulationID)
	}
	return nil
}

func findEvent(root map[string]any, kind string) map[string]any {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return nil
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return event
		}
	}
	return nil
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

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) publishRegulation(model string, tolerance float64) (string, error) {
	response, err := h.request(http.MethodPost, "/regulations", map[string]any{
		"device_model": model,
		"tolerance":    tolerance,
		"published_by": "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "regulation", "id")
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	if _, err := h.publishRegulation("Bench Meter", 0.5); err != nil {
		return "", err
	}
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
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
	if _, err := h.publishRegulation("Bench Meter", 0.5); err != nil {
		return "", err
	}
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
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

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
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

func nestedStringAt(root map[string]any, path ...any) (string, error) {
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

func nestedNumber(root map[string]any, path ...any) (float64, error) {
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

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
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
