package washup

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// EntryView pairs an entry with its correction chain. QuantityKG on the
// embedded Entry is always the originally recorded value; EffectiveKG is
// what reconciliation uses.
type EntryView struct {
	Entry       Entry        `json:"entry"`
	EffectiveKG float64      `json:"effective_kg"`
	Corrections []Correction `json:"corrections,omitempty"`
}

type CircuitBalance struct {
	Circuit      string  `json:"circuit"`
	RefillKG     float64 `json:"refill_kg"`
	RecoveryKG   float64 `json:"recovery_kg"`
	WasteKG      float64 `json:"waste_kg"`
	DifferenceKG float64 `json:"difference_kg"`
	Abnormal     bool    `json:"abnormal"`
}

type SwitchBalance struct {
	SwitchID     string    `json:"switch_id"`
	JobID        string    `json:"job_id"`
	FromColor    string    `json:"from_color"`
	ToColor      string    `json:"to_color"`
	OccurredAt   time.Time `json:"occurred_at"`
	Entries      int       `json:"entries"`
	RefillKG     float64   `json:"refill_kg"`
	RecoveryKG   float64   `json:"recovery_kg"`
	WasteKG      float64   `json:"waste_kg"`
	DifferenceKG float64   `json:"difference_kg"`
	Abnormal     bool      `json:"abnormal"`
}

type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	ToleranceKG float64          `json:"tolerance_kg"`
	Circuits    []CircuitBalance `json:"circuits"`
	Switches    []SwitchBalance  `json:"switches"`
}

type Ledger struct {
	mu          sync.RWMutex
	store       *Store
	tolerance   float64
	entries     map[string]Entry
	entryOrder  []string
	corrections map[string][]Correction
	switches    map[string]Switch
	switchOrder []string
}

// Open replays the append-only store and rebuilds the in-memory ledger.
func Open(store *Store, toleranceKG float64) (*Ledger, error) {
	if store == nil || toleranceKG <= 0 {
		return nil, fmt.Errorf("washup ledger configuration is incomplete")
	}
	ledger := &Ledger{store: store, tolerance: toleranceKG, entries: map[string]Entry{}, corrections: map[string][]Correction{}, switches: map[string]Switch{}}
	records, err := store.Records()
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		switch rec.RecordKind {
		case recordEntry:
			if err := ledger.indexEntry(*rec.Entry); err != nil {
				return nil, err
			}
		case recordCorrection:
			if err := ledger.indexCorrection(*rec.Correction); err != nil {
				return nil, err
			}
		case recordSwitch:
			if err := ledger.indexSwitch(*rec.Switch); err != nil {
				return nil, err
			}
		}
	}
	return ledger, nil
}

func (ledger *Ledger) Append(entry Entry) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.checkSwitchLocked(entry.SwitchID); err != nil {
		return err
	}
	if err := ledger.store.Append(record{RecordKind: recordEntry, Entry: &entry}); err != nil {
		return err
	}
	return ledger.indexEntryLocked(entry)
}

func (ledger *Ledger) RecordSwitch(sw Switch) error {
	if err := sw.Validate(); err != nil {
		return err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.store.Append(record{RecordKind: recordSwitch, Switch: &sw}); err != nil {
		return err
	}
	return ledger.indexSwitchLocked(sw)
}

// Correct appends a correction for an existing entry. The original record
// stays untouched in the store; the correction joins its audit chain.
func (ledger *Ledger) Correct(correction Correction) error {
	if err := correction.Validate(); err != nil {
		return err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if _, ok := ledger.entries[correction.EntryID]; !ok {
		return fmt.Errorf("washup entry %s does not exist", correction.EntryID)
	}
	if err := ledger.store.Append(record{RecordKind: recordCorrection, Correction: &correction}); err != nil {
		return err
	}
	return ledger.indexCorrectionLocked(correction)
}

func (ledger *Ledger) Entries(circuit string) []EntryView {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	views := []EntryView{}
	for _, id := range ledger.entryOrder {
		entry := ledger.entries[id]
		if circuit != "" && entry.Circuit != circuit {
			continue
		}
		views = append(views, ledger.viewLocked(entry))
	}
	sort.SliceStable(views, func(left, right int) bool {
		if views[left].Entry.RecordedAt.Equal(views[right].Entry.RecordedAt) {
			return views[left].Entry.ID < views[right].Entry.ID
		}
		return views[left].Entry.RecordedAt.Before(views[right].Entry.RecordedAt)
	})
	return views
}

func (ledger *Ledger) Entry(id string) (EntryView, bool) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	entry, ok := ledger.entries[id]
	if !ok {
		return EntryView{}, false
	}
	return ledger.viewLocked(entry), true
}

func (ledger *Ledger) Switches() []Switch {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	values := []Switch{}
	for _, id := range ledger.switchOrder {
		values = append(values, ledger.switches[id])
	}
	sort.SliceStable(values, func(left, right int) bool { return values[left].OccurredAt.Before(values[right].OccurredAt) })
	return values
}

// Report reconciles effective quantities per circuit and per changeover.
// A difference beyond tolerance (either direction) is flagged abnormal and
// carries its switch identity so the loss can be traced to that transition.
func (ledger *Ledger) Report(circuit string, now time.Time) Report {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	report := Report{GeneratedAt: now.UTC(), ToleranceKG: ledger.tolerance, Circuits: []CircuitBalance{}, Switches: []SwitchBalance{}}
	totals := map[string]*CircuitBalance{}
	for _, id := range ledger.entryOrder {
		entry := ledger.entries[id]
		if circuit != "" && entry.Circuit != circuit {
			continue
		}
		balance, ok := totals[entry.Circuit]
		if !ok {
			balance = &CircuitBalance{Circuit: entry.Circuit}
			totals[entry.Circuit] = balance
		}
		ledger.accrueLocked(&balance.RefillKG, &balance.RecoveryKG, &balance.WasteKG, entry)
	}
	for _, balance := range totals {
		balance.DifferenceKG = roundKG(balance.RefillKG - balance.RecoveryKG - balance.WasteKG)
		balance.Abnormal = math.Abs(balance.DifferenceKG) > ledger.tolerance
		report.Circuits = append(report.Circuits, *balance)
	}
	sort.Slice(report.Circuits, func(left, right int) bool { return report.Circuits[left].Circuit < report.Circuits[right].Circuit })
	for _, id := range ledger.switchOrder {
		sw := ledger.switches[id]
		balance := SwitchBalance{SwitchID: sw.ID, JobID: sw.JobID, FromColor: sw.FromColor, ToColor: sw.ToColor, OccurredAt: sw.OccurredAt}
		for _, entryID := range ledger.entryOrder {
			entry := ledger.entries[entryID]
			if entry.SwitchID != sw.ID || (circuit != "" && entry.Circuit != circuit) {
				continue
			}
			balance.Entries++
			ledger.accrueLocked(&balance.RefillKG, &balance.RecoveryKG, &balance.WasteKG, entry)
		}
		balance.DifferenceKG = roundKG(balance.RefillKG - balance.RecoveryKG - balance.WasteKG)
		balance.Abnormal = math.Abs(balance.DifferenceKG) > ledger.tolerance
		report.Switches = append(report.Switches, balance)
	}
	return report
}

func (ledger *Ledger) accrueLocked(refill, recovery, waste *float64, entry Entry) {
	effective := ledger.effectiveLocked(entry.ID)
	switch entry.Kind {
	case EntryRefill:
		*refill = roundKG(*refill + effective)
	case EntryRecovery:
		*recovery = roundKG(*recovery + effective)
	case EntryWaste:
		*waste = roundKG(*waste + effective)
	}
}

func (ledger *Ledger) viewLocked(entry Entry) EntryView {
	chain := append([]Correction(nil), ledger.corrections[entry.ID]...)
	sort.SliceStable(chain, func(left, right int) bool { return chain[left].CorrectedAt.Before(chain[right].CorrectedAt) })
	return EntryView{Entry: entry, EffectiveKG: ledger.effectiveLocked(entry.ID), Corrections: chain}
}

func (ledger *Ledger) effectiveLocked(entryID string) float64 {
	entry := ledger.entries[entryID]
	chain := ledger.corrections[entryID]
	if len(chain) == 0 {
		return entry.QuantityKG
	}
	latest := chain[0]
	for _, correction := range chain[1:] {
		if correction.CorrectedAt.After(latest.CorrectedAt) {
			latest = correction
		}
	}
	return latest.QuantityKG
}

func (ledger *Ledger) checkSwitchLocked(switchID string) error {
	if switchID == "" {
		return nil
	}
	if _, ok := ledger.switches[switchID]; !ok {
		return fmt.Errorf("washup switch %s does not exist", switchID)
	}
	return nil
}

func (ledger *Ledger) indexEntry(entry Entry) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.indexEntryLocked(entry)
}

func (ledger *Ledger) indexEntryLocked(entry Entry) error {
	if _, ok := ledger.entries[entry.ID]; ok {
		return fmt.Errorf("washup entry %s is duplicated", entry.ID)
	}
	if err := ledger.checkSwitchLocked(entry.SwitchID); err != nil {
		return err
	}
	ledger.entries[entry.ID] = entry
	ledger.entryOrder = append(ledger.entryOrder, entry.ID)
	return nil
}

func (ledger *Ledger) indexCorrection(correction Correction) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.indexCorrectionLocked(correction)
}

func (ledger *Ledger) indexCorrectionLocked(correction Correction) error {
	if _, ok := ledger.entries[correction.EntryID]; !ok {
		return fmt.Errorf("washup correction targets unknown entry %s", correction.EntryID)
	}
	ledger.corrections[correction.EntryID] = append(ledger.corrections[correction.EntryID], correction)
	return nil
}

func (ledger *Ledger) indexSwitch(sw Switch) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.indexSwitchLocked(sw)
}

func (ledger *Ledger) indexSwitchLocked(sw Switch) error {
	if _, ok := ledger.switches[sw.ID]; ok {
		return fmt.Errorf("washup switch %s is duplicated", sw.ID)
	}
	ledger.switches[sw.ID] = sw
	ledger.switchOrder = append(ledger.switchOrder, sw.ID)
	return nil
}

func roundKG(value float64) float64 {
	return math.Round(value*1000) / 1000
}
