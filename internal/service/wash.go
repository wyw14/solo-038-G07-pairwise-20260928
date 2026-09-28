package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/wyw14/solo-038/internal/wash"
)

// WeighRequest is a cleaning-fluid weigh-in against one color-change job. The
// switch may be omitted to book against the most recent changeover; the ink
// route (circuit) is always taken from that switch, never supplied by the
// caller, so a reading cannot be filed against the wrong route.
type WeighRequest struct {
	SwitchID   string     `json:"switch_id,omitempty"`
	Kind       string     `json:"kind"`
	MassG      float64    `json:"mass_g"`
	Container  string     `json:"container,omitempty"`
	Operator   string     `json:"operator"`
	MeasuredAt *time.Time `json:"measured_at,omitempty"`
}

// CorrectionRequest replaces one earlier weigh-in. The original record is
// retained; a new entry is appended carrying the reason.
type CorrectionRequest struct {
	EntryID   string  `json:"entry_id"`
	MassG     float64 `json:"mass_g"`
	Container string  `json:"container,omitempty"`
	Operator  string  `json:"operator"`
	Reason    string  `json:"reason"`
}

// WashLedger is the full reconciliation view returned to operators.
type WashLedger = wash.Ledger

// WashLedgerView returns every changeover with its mass balance, totals and
// the changeovers whose fluid loss is unexplained (missing weigh-in or
// out-of-tolerance shortfall).
func (runtime *Runtime) WashLedger() wash.Ledger {
	return runtime.wash.State().Reconcile(runtime.washToleranceG)
}

// WashSwitchDetail returns one changeover: its balance, the raw records
// (originals and corrections) and the correction chains per flow kind so a
// correction can be audited back to the original reading and reason.
func (runtime *Runtime) WashSwitchDetail(switchID string) (map[string]any, error) {
	id := wash.SwitchID(switchID)
	if !id.Valid() {
		return nil, fmt.Errorf("switch id is invalid")
	}
	event, ok := runtime.wash.State().Switch(id)
	if !ok {
		return nil, fmt.Errorf("wash switch %s: %w", switchID, wash.ErrSwitchNotFound)
	}
	balance, err := runtime.wash.State().Balance(id, runtime.washToleranceG)
	if err != nil {
		return nil, err
	}
	chains := map[string][]wash.Entry{}
	for _, kind := range []wash.FlowKind{wash.FlowReplenish, wash.FlowRecovered, wash.FlowWaste} {
		chains[string(kind)] = runtime.wash.State().CorrectionChain(id, kind)
	}
	return map[string]any{
		"switch":  event,
		"balance": balance,
		"records": runtime.wash.State().Entries(id),
		"chains":  chains,
	}, nil
}

// RecordWeighing books one replenish/recovered/waste measurement against a
// color-change job.
func (runtime *Runtime) RecordWeighing(request WeighRequest) (wash.Entry, error) {
	kind := wash.FlowKind(strings.TrimSpace(request.Kind))
	if !kind.Valid() {
		return wash.Entry{}, fmt.Errorf("weighing kind must be one of replenish, recovered, waste")
	}
	if strings.TrimSpace(request.Operator) == "" {
		return wash.Entry{}, fmt.Errorf("operator is required")
	}
	event, err := runtime.resolveWashSwitch(request.SwitchID)
	if err != nil {
		return wash.Entry{}, err
	}
	measuredAt := time.Now().UTC()
	if request.MeasuredAt != nil {
		measuredAt = request.MeasuredAt.UTC()
	}
	entry := wash.Entry{
		ID:         wash.NewEntryID(),
		SwitchID:   event.ID,
		Circuit:    event.Circuit,
		Kind:       kind,
		MassG:      request.MassG,
		Container:  strings.TrimSpace(request.Container),
		Operator:   strings.TrimSpace(request.Operator),
		MeasuredAt: measuredAt,
		RecordedAt: time.Now().UTC(),
	}
	if _, err := runtime.wash.RecordEntry(&entry); err != nil {
		return wash.Entry{}, err
	}
	return entry, nil
}

// CorrectWeighing appends a correction for an existing entry. The entry's
// switch, circuit and flow kind are inherited; only the measurement (and
// optionally container/operator) is replaced, with a mandatory reason.
func (runtime *Runtime) CorrectWeighing(request CorrectionRequest) (wash.Entry, error) {
	targetID := wash.EntryID(strings.TrimSpace(request.EntryID))
	if !targetID.Valid() {
		return wash.Entry{}, fmt.Errorf("entry id is invalid")
	}
	if strings.TrimSpace(request.Reason) == "" {
		return wash.Entry{}, fmt.Errorf("correction reason is required")
	}
	if strings.TrimSpace(request.Operator) == "" {
		return wash.Entry{}, fmt.Errorf("operator is required")
	}
	target, ok := runtime.wash.State().Entry(targetID)
	if !ok {
		return wash.Entry{}, fmt.Errorf("wash entry %s: %w", request.EntryID, wash.ErrEntryNotFound)
	}
	now := time.Now().UTC()
	container := target.Container
	if strings.TrimSpace(request.Container) != "" {
		container = strings.TrimSpace(request.Container)
	}
	correction := wash.Entry{
		ID:         wash.NewEntryID(),
		SwitchID:   target.SwitchID,
		Circuit:    target.Circuit,
		Kind:       target.Kind,
		MassG:      request.MassG,
		Container:  container,
		Operator:   strings.TrimSpace(request.Operator),
		MeasuredAt: now,
		RecordedAt: now,
		Supersedes: target.ID,
		Reason:     strings.TrimSpace(request.Reason),
	}
	if _, err := runtime.wash.RecordEntry(&correction); err != nil {
		return wash.Entry{}, err
	}
	return correction, nil
}

// resolveWashSwitch finds the target changeover: an explicit id, or the most
// recently recorded switch when none is given.
func (runtime *Runtime) resolveWashSwitch(switchID string) (wash.Switch, error) {
	if strings.TrimSpace(switchID) != "" {
		id := wash.SwitchID(strings.TrimSpace(switchID))
		if !id.Valid() {
			return wash.Switch{}, fmt.Errorf("switch id is invalid")
		}
		event, ok := runtime.wash.State().Switch(id)
		if !ok {
			return wash.Switch{}, fmt.Errorf("wash switch %s: %w", switchID, wash.ErrSwitchNotFound)
		}
		return event, nil
	}
	switches := runtime.wash.State().Switches()
	if len(switches) == 0 {
		return wash.Switch{}, fmt.Errorf("no color change recorded: %w", wash.ErrSwitchNotFound)
	}
	return switches[len(switches)-1], nil
}
