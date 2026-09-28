package washup

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type EntryKind string

const (
	EntryRefill   EntryKind = "refill"
	EntryRecovery EntryKind = "recovery"
	EntryWaste    EntryKind = "waste"
)

var entryKinds = map[EntryKind]bool{
	EntryRefill:   true,
	EntryRecovery: true,
	EntryWaste:    true,
}

// Entry is one cleaning-fluid ledger line: a refill into a circuit, a
// recovery back to supply, or a waste-barrel weighing.
type Entry struct {
	ID         string    `json:"id"`
	Circuit    string    `json:"circuit"`
	Kind       EntryKind `json:"kind"`
	QuantityKG float64   `json:"quantity_kg"`
	SwitchID   string    `json:"switch_id,omitempty"`
	Barrel     string    `json:"barrel,omitempty"`
	Operator   string    `json:"operator,omitempty"`
	Note       string    `json:"note,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

func NewEntry(circuit string, kind EntryKind, quantityKG float64, now time.Time) Entry {
	return Entry{ID: uuid.NewString(), Circuit: circuit, Kind: kind, QuantityKG: quantityKG, RecordedAt: now.UTC()}
}

func (entry Entry) Validate() error {
	if entry.ID == "" || entry.Circuit == "" {
		return errors.New("washup entry identity is incomplete")
	}
	if !entryKinds[entry.Kind] {
		return fmt.Errorf("washup entry kind %q is unsupported", entry.Kind)
	}
	if entry.QuantityKG <= 0 {
		return errors.New("washup entry quantity must be positive")
	}
	if entry.Barrel != "" && entry.Kind != EntryWaste {
		return errors.New("washup barrel reference requires a waste entry")
	}
	if entry.RecordedAt.IsZero() {
		return errors.New("washup entry timestamp is required")
	}
	return nil
}

// Correction supersedes an entry's quantity. The original entry is never
// modified; the correction chain is the audit trail.
type Correction struct {
	ID          string    `json:"id"`
	EntryID     string    `json:"entry_id"`
	QuantityKG  float64   `json:"quantity_kg"`
	Reason      string    `json:"reason"`
	CorrectedAt time.Time `json:"corrected_at"`
}

func NewCorrection(entryID string, quantityKG float64, reason string, now time.Time) Correction {
	return Correction{ID: uuid.NewString(), EntryID: entryID, QuantityKG: quantityKG, Reason: reason, CorrectedAt: now.UTC()}
}

func (correction Correction) Validate() error {
	if correction.ID == "" || correction.EntryID == "" {
		return errors.New("washup correction identity is incomplete")
	}
	if correction.QuantityKG <= 0 {
		return errors.New("washup correction quantity must be positive")
	}
	if correction.Reason == "" {
		return errors.New("washup correction reason is required")
	}
	if correction.CorrectedAt.IsZero() {
		return errors.New("washup correction timestamp is required")
	}
	return nil
}

// Switch anchors a color changeover so that cleaning-fluid quantities can be
// reconciled against the specific transition that purged the circuit.
type Switch struct {
	ID         string    `json:"id"`
	JobID      string    `json:"job_id"`
	FromColor  string    `json:"from_color"`
	ToColor    string    `json:"to_color"`
	OccurredAt time.Time `json:"occurred_at"`
}

func NewSwitch(jobID, fromColor, toColor string, now time.Time) Switch {
	return Switch{ID: uuid.NewString(), JobID: jobID, FromColor: fromColor, ToColor: toColor, OccurredAt: now.UTC()}
}

func (sw Switch) Validate() error {
	if sw.ID == "" || sw.JobID == "" {
		return errors.New("washup switch identity is incomplete")
	}
	if sw.FromColor == "" || sw.ToColor == "" || sw.FromColor == sw.ToColor {
		return errors.New("washup switch colors are invalid")
	}
	if sw.OccurredAt.IsZero() {
		return errors.New("washup switch timestamp is required")
	}
	return nil
}
