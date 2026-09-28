package wash

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	// ErrSwitchNotFound means a weigh-in referenced a changeover that was never
	// recorded or has not been replayed yet.
	ErrSwitchNotFound = errors.New("wash switch not registered")
	// ErrEntryNotFound means a correction targeted a weigh-in that does not exist.
	ErrEntryNotFound = errors.New("wash entry not registered")
)

// State is the append-only wash ledger. It is safe for concurrent use.
// Records are never mutated or deleted: a correction appends a new Entry that
// supersedes the original, which stays readable with its original value.
type State struct {
	mu         sync.RWMutex
	switches   map[SwitchID]Switch
	switchID   []SwitchID
	entries    map[EntryID]Entry
	entryID    []EntryID
	superseded map[EntryID]bool
}

func NewState() *State {
	return &State{switches: map[SwitchID]Switch{}, entries: map[EntryID]Entry{}, superseded: map[EntryID]bool{}}
}

// RecordSwitch registers a color-change job. Sequence is assigned by the
// caller (the durable store) and reflects append order across reloads.
func (state *State) RecordSwitch(event Switch) error {
	if err := event.Validate(); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, exists := state.switches[event.ID]; exists {
		return fmt.Errorf("wash switch %s already recorded", event.ID)
	}
	state.switches[event.ID] = event
	state.switchID = append(state.switchID, event.ID)
	return nil
}

// RecordEntry appends a weigh-in record (or a correction that supersedes an
// earlier one). The switch must already be registered, the circuit must match
// the one declared on the switch, and a correction must target a live record
// of the same switch and flow kind.
func (state *State) RecordEntry(entry Entry) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkEntryLocked(entry); err != nil {
		return err
	}
	if entry.IsCorrection() {
		state.superseded[entry.Supersedes] = true
	}
	state.entries[entry.ID] = entry
	state.entryID = append(state.entryID, entry.ID)
	return nil
}

// previewEntry validates an entry against the current ledger without mutating
// it, so the durable store can reject a bad record before it touches disk.
func (state *State) previewEntry(entry Entry) error {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.checkEntryLocked(entry)
}

// checkEntryLocked runs the ledger-level checks. The caller must hold at least
// a read lock; mutation callers hold the write lock.
func (state *State) checkEntryLocked(entry Entry) error {
	if _, exists := state.entries[entry.ID]; exists {
		return fmt.Errorf("wash entry %s already recorded", entry.ID)
	}
	event, ok := state.switches[entry.SwitchID]
	if !ok {
		return fmt.Errorf("wash entry switch %s: %w", entry.SwitchID, ErrSwitchNotFound)
	}
	if entry.Circuit != event.Circuit {
		return fmt.Errorf("wash entry circuit %s does not match switch circuit %s", entry.Circuit, event.Circuit)
	}
	if entry.IsCorrection() {
		target, ok := state.entries[entry.Supersedes]
		if !ok {
			return fmt.Errorf("corrected wash entry %s: %w", entry.Supersedes, ErrEntryNotFound)
		}
		if target.SwitchID != entry.SwitchID || target.Kind != entry.Kind {
			return fmt.Errorf("correction must match the original switch and flow kind")
		}
		if state.superseded[target.ID] {
			return fmt.Errorf("wash entry %s has already been corrected", target.ID)
		}
	}
	return nil
}

func (state *State) Switch(id SwitchID) (Switch, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	event, ok := state.switches[id]
	return event, ok
}

func (state *State) hasSwitch(id SwitchID) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	_, ok := state.switches[id]
	return ok
}

// Switches returns all color-change jobs in append (chronological) order.
func (state *State) Switches() []Switch {
	state.mu.RLock()
	defer state.mu.RUnlock()
	values := make([]Switch, 0, len(state.switchID))
	for _, id := range state.switchID {
		values = append(values, state.switches[id])
	}
	return values
}

// Entry returns the raw record by id. Corrections do not remove the original,
// so the original reading stays retrievable here.
func (state *State) Entry(id EntryID) (Entry, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	entry, ok := state.entries[id]
	return entry, ok
}

// Entries returns every raw record (originals and corrections) in append
// order, optionally restricted to one switch.
func (state *State) Entries(switchID SwitchID) []Entry {
	state.mu.RLock()
	defer state.mu.RUnlock()
	values := make([]Entry, 0, len(state.entryID))
	for _, id := range state.entryID {
		entry := state.entries[id]
		if switchID != "" && entry.SwitchID != switchID {
			continue
		}
		values = append(values, entry)
	}
	return values
}

// CorrectionChain returns the correction lineage for a switch and flow kind:
// the original record followed by each correction, in append order. The final
// element is the currently effective reading.
func (state *State) CorrectionChain(switchID SwitchID, kind FlowKind) []Entry {
	state.mu.RLock()
	defer state.mu.RUnlock()
	var roots []EntryID
	rootOf := map[EntryID]EntryID{}
	for _, id := range state.entryID {
		entry := state.entries[id]
		if entry.SwitchID != switchID || entry.Kind != kind {
			continue
		}
		if entry.IsCorrection() {
			root := entry.Supersedes
			if parent, ok := rootOf[entry.Supersedes]; ok {
				root = parent
			}
			rootOf[entry.ID] = root
		} else {
			roots = append(roots, entry.ID)
			rootOf[entry.ID] = entry.ID
		}
	}
	var chain []Entry
	for _, root := range roots {
		for _, id := range state.entryID {
			entry := state.entries[id]
			if entry.SwitchID != switchID || entry.Kind != kind {
				continue
			}
			r := rootOf[entry.ID]
			if r == "" {
				r = entry.ID
			}
			if r == root {
				chain = append(chain, entry)
			}
		}
	}
	return chain
}

// effectiveTotals sums the live readings per flow kind for a switch. Multiple
// weigh-ins of the same kind (e.g. two waste drums) add together. A record that
// has been superseded by a correction contributes nothing; the correction does.
func (state *State) effectiveTotals(switchID SwitchID) map[FlowKind]float64 {
	state.mu.RLock()
	defer state.mu.RUnlock()
	totals := map[FlowKind]float64{}
	seen := map[FlowKind]bool{}
	for _, id := range state.entryID {
		entry := state.entries[id]
		if entry.SwitchID != switchID {
			continue
		}
		seen[entry.Kind] = true
		if state.superseded[entry.ID] {
			continue
		}
		totals[entry.Kind] += entry.MassG
	}
	for kind := range seen {
		if _, ok := totals[kind]; !ok {
			totals[kind] = 0
		}
	}
	return totals
}

// presentKinds reports which flow kinds have at least one (even if superseded)
// record on the switch. A corrected reading still counts as evidence; the
// original is not "missing" just because it was corrected.
func (state *State) presentKinds(switchID SwitchID) map[FlowKind]bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	present := map[FlowKind]bool{}
	for _, id := range state.entryID {
		entry := state.entries[id]
		if entry.SwitchID == switchID {
			present[entry.Kind] = true
		}
	}
	return present
}

// Balance is the fluid mass-balance for one color-change job.
type Balance struct {
	Switch     Switch  `json:"switch"`
	ReplenishG float64 `json:"replenish_g"`
	RecoveredG float64 `json:"recovered_g"`
	WasteG     float64 `json:"waste_g"`
	// ShortfallG = replenish - recovered - waste. Positive mass that left the
	// route but is accounted for by neither recovery nor the waste drum.
	ShortfallG  float64  `json:"shortfall_g"`
	Balanced    bool     `json:"balanced"`
	Missing     []string `json:"missing_kinds"`
	AnomalyNote string   `json:"anomaly_note,omitempty"`
}

// Balance computes the mass balance for one switch using currently effective
// readings. toleranceG is the absolute shortfall that is still treated as
// balanced. Kinds that were never weighed are reported as missing evidence.
func (state *State) Balance(id SwitchID, toleranceG float64) (Balance, error) {
	state.mu.RLock()
	event, ok := state.switches[id]
	state.mu.RUnlock()
	if !ok {
		return Balance{}, fmt.Errorf("wash switch %s: %w", id, ErrSwitchNotFound)
	}
	totals := state.effectiveTotals(id)
	present := state.presentKinds(id)
	balance := Balance{Switch: event}
	if present[FlowReplenish] {
		balance.ReplenishG = totals[FlowReplenish]
	} else {
		balance.Missing = append(balance.Missing, string(FlowReplenish))
	}
	if present[FlowRecovered] {
		balance.RecoveredG = totals[FlowRecovered]
	} else {
		balance.Missing = append(balance.Missing, string(FlowRecovered))
	}
	if present[FlowWaste] {
		balance.WasteG = totals[FlowWaste]
	} else {
		balance.Missing = append(balance.Missing, string(FlowWaste))
	}
	balance.ShortfallG = balance.ReplenishG - balance.RecoveredG - balance.WasteG
	balance.Balanced = len(balance.Missing) == 0 && abs(balance.ShortfallG) <= toleranceG
	balance.AnomalyNote = event.AnomalyNote
	return balance, nil
}

// Ledger is the full reconciliation view: every switch in chronological order
// plus aggregate totals and the switches whose loss cannot be explained.
type Ledger struct {
	ToleranceG     float64   `json:"tolerance_g"`
	TotalReplenish float64   `json:"total_replenish_g"`
	TotalRecovered float64   `json:"total_recovered_g"`
	TotalWaste     float64   `json:"total_waste_g"`
	TotalShortfall float64   `json:"total_shortfall_g"`
	Balances       []Balance `json:"balances"`
	// Anomalies lists switches with missing weigh-ins or an out-of-tolerance
	// shortfall, worst shortfall first, so abnormal loss maps to a changeover.
	Anomalies []Balance `json:"anomalies"`
}

// Reconcile builds the balance for every switch and flags anomalies.
func (state *State) Reconcile(toleranceG float64) Ledger {
	ledger := Ledger{ToleranceG: toleranceG, Balances: []Balance{}, Anomalies: []Balance{}}
	for _, event := range state.Switches() {
		balance, err := state.Balance(event.ID, toleranceG)
		if err != nil {
			continue
		}
		ledger.Balances = append(ledger.Balances, balance)
		ledger.TotalReplenish += balance.ReplenishG
		ledger.TotalRecovered += balance.RecoveredG
		ledger.TotalWaste += balance.WasteG
		ledger.TotalShortfall += balance.ShortfallG
		if len(balance.Missing) > 0 || !balance.Balanced {
			ledger.Anomalies = append(ledger.Anomalies, balance)
		}
	}
	sort.SliceStable(ledger.Anomalies, func(left, right int) bool {
		return ledger.Anomalies[left].ShortfallG > ledger.Anomalies[right].ShortfallG
	})
	return ledger
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
