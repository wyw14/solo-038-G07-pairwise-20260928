package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wyw14/solo-038/internal/service"
)

func newTestRuntime(t *testing.T) *service.Runtime {
	t.Helper()
	runtime, err := service.NewRuntime(service.Config{
		DataDir:           filepath.Join(t.TempDir(), "data"),
		FestoonCapacity:   120,
		RegisterGap:       500 * time.Millisecond,
		RegisterTolerance: 0.25,
		WashToleranceG:    50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func doJSON(t *testing.T, router http.Handler, method, target string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var decoded map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response was not JSON: %s", recorder.Body.String())
		}
	}
	return recorder.Code, decoded
}

func TestWashLedgerEndToEnd(t *testing.T) {
	runtime := newTestRuntime(t)
	router := NewRouter(runtime)

	// Performing a color change registers the changeover in the wash ledger.
	if _, err := runtime.CreateOperation(context.Background(), "switch_ink"); err != nil {
		t.Fatalf("switch_ink failed: %v", err)
	}

	status, ledger := doJSON(t, router, http.MethodGet, "/api/wash-ledger/", nil)
	if status != http.StatusOK {
		t.Fatalf("ledger status = %d", status)
	}
	balances, _ := ledger["balances"].([]any)
	if len(balances) != 1 {
		t.Fatalf("expected 1 switch balance, got %v", ledger["balances"])
	}
	// With no weigh-ins yet, the changeover is an anomaly missing all evidence.
	anomalies, _ := ledger["anomalies"].([]any)
	if len(anomalies) != 1 {
		t.Fatalf("fresh changeover should be an anomaly, got %v", ledger["anomalies"])
	}
	first := balances[0].(map[string]any)
	switchValue := first["switch"].(map[string]any)
	switchID := switchValue["id"].(string)
	if switchValue["from_color"] != "cyan" || switchValue["to_color"] != "magenta" {
		t.Fatalf("unexpected switch colors: %v", switchValue)
	}

	// Book the three weighings against the changeover.
	for _, weighing := range []map[string]any{
		{"switch_id": switchID, "kind": "replenish", "mass_g": 1000, "operator": "zhao", "container": "fluid-can"},
		{"switch_id": switchID, "kind": "recovered", "mass_g": 700, "operator": "zhao", "container": "recycle-tote"},
		{"switch_id": switchID, "kind": "waste", "mass_g": 250, "operator": "zhao", "container": "waste-drum"},
	} {
		status, _ := doJSON(t, router, http.MethodPost, "/api/wash-ledger/weighings", weighing)
		if status != http.StatusCreated {
			t.Fatalf("weighing %v -> status %d", weighing["kind"], status)
		}
	}
	// 1000 - 700 - 250 = 50 g, within the 50 g tolerance: no anomaly.
	_, ledger = doJSON(t, router, http.MethodGet, "/api/wash-ledger/", nil)
	if anomalies, _ := ledger["anomalies"].([]any); len(anomalies) != 0 {
		t.Fatalf("balanced changeover still flagged: %v", anomalies)
	}

	// Correct the 250 waste reading downward to make the shortfall 100.
	detailStatus, detail := doJSON(t, router, http.MethodGet, "/api/wash-ledger/switches/"+switchID, nil)
	if detailStatus != http.StatusOK {
		t.Fatalf("detail status = %d", detailStatus)
	}
	var wasteEntryID string
	for _, record := range detail["records"].([]any) {
		entry := record.(map[string]any)
		if entry["kind"] == "waste" {
			wasteEntryID = entry["id"].(string)
		}
	}
	if wasteEntryID == "" {
		t.Fatal("waste entry not found in detail records")
	}
	// Correction without a reason is rejected (original must stay authoritative).
	reasonless := map[string]any{"entry_id": wasteEntryID, "mass_g": 200, "operator": "zhao"}
	if status, body := doJSON(t, router, http.MethodPost, "/api/wash-ledger/corrections", reasonless); status != http.StatusBadRequest {
		t.Fatalf("reasonless correction status = %d body=%v", status, body)
	}
	correction := map[string]any{"entry_id": wasteEntryID, "mass_g": 200, "operator": "zhao", "reason": "scale not tared; reweighed drum"}
	if status, body := doJSON(t, router, http.MethodPost, "/api/wash-ledger/corrections", correction); status != http.StatusCreated {
		t.Fatalf("correction status = %d body=%v", status, body)
	}
	// The original 250 record survives and the chain exposes it with the reason.
	_, detail = doJSON(t, router, http.MethodGet, "/api/wash-ledger/switches/"+switchID, nil)
	records, _ := detail["records"].([]any)
	var original, corrected map[string]any
	for _, record := range records {
		entry := record.(map[string]any)
		if entry["kind"] != "waste" {
			continue
		}
		if entry["id"] == wasteEntryID {
			original = entry
		} else {
			corrected = entry
		}
	}
	if original == nil || original["mass_g"] != float64(250) {
		t.Fatalf("original waste record not retained unchanged: %v", original)
	}
	if corrected == nil || corrected["mass_g"] != float64(200) || corrected["reason"] != correction["reason"] {
		t.Fatalf("correction missing value/reason: %v", corrected)
	}
	// Shortfall is now 100 > 50 tolerance -> anomaly located at this switch.
	_, ledger = doJSON(t, router, http.MethodGet, "/api/wash-ledger/", nil)
	anomalies, _ = ledger["anomalies"].([]any)
	if len(anomalies) != 1 {
		t.Fatalf("expected one anomaly after correction, got %v", ledger["anomalies"])
	}
	located := anomalies[0].(map[string]any)["switch"].(map[string]any)["id"]
	if located != switchID {
		t.Fatalf("anomaly located at %v, want %s", located, switchID)
	}

	// Unknown switch -> 404; bad kind -> 400.
	if status, _ := doJSON(t, router, http.MethodGet, "/api/wash-ledger/switches/00000000-0000-0000-0000-000000000000", nil); status != http.StatusNotFound {
		t.Fatalf("missing switch status = %d, want 404", status)
	}
	if status, _ := doJSON(t, router, http.MethodPost, "/api/wash-ledger/weighings", map[string]any{"kind": "evaporated", "mass_g": 1, "operator": "x"}); status != http.StatusBadRequest {
		t.Fatalf("bad kind status = %d, want 400", status)
	}
}

func TestWashLedgerSurvivesRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	first, err := service.NewRuntime(service.Config{DataDir: dataDir, FestoonCapacity: 120, RegisterGap: time.Millisecond, RegisterTolerance: 0.25, WashToleranceG: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateOperation(context.Background(), "switch_ink"); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(first)
	if status, _ := doJSON(t, router, http.MethodPost, "/api/wash-ledger/weighings", map[string]any{"kind": "replenish", "mass_g": 500, "operator": "q"}); status != http.StatusCreated {
		t.Fatalf("replenish status = %d", status)
	}
	if status, _ := doJSON(t, router, http.MethodPost, "/api/wash-ledger/weighings", map[string]any{"kind": "recovered", "mass_g": 300, "operator": "q"}); status != http.StatusCreated {
		t.Fatalf("recovered status = %d", status)
	}
	if status, _ := doJSON(t, router, http.MethodPost, "/api/wash-ledger/weighings", map[string]any{"kind": "waste", "mass_g": 200, "operator": "q"}); status != http.StatusCreated {
		t.Fatalf("waste status = %d", status)
	}

	// A brand-new runtime over the same data directory replays the ledger.
	second, err := service.NewRuntime(service.Config{DataDir: dataDir, FestoonCapacity: 120, RegisterGap: time.Millisecond, RegisterTolerance: 0.25, WashToleranceG: 0})
	if err != nil {
		t.Fatal(err)
	}
	ledger := second.WashLedger()
	if len(ledger.Balances) != 1 {
		t.Fatalf("replayed balances = %d", len(ledger.Balances))
	}
	balance := ledger.Balances[0]
	if balance.ReplenishG != 500 || balance.RecoveredG != 300 || balance.WasteG != 200 {
		t.Fatalf("replayed totals = %+v", balance)
	}
	if balance.ShortfallG != 0 || !balance.Balanced {
		t.Fatalf("replayed switch should balance exactly: %+v", balance)
	}
}
