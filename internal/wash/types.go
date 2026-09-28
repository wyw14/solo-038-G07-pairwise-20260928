package wash

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type SwitchID string
type EntryID string

func NewSwitchID() SwitchID { return SwitchID(uuid.NewString()) }
func NewEntryID() EntryID   { return EntryID(uuid.NewString()) }

func (id SwitchID) Valid() bool {
	_, err := uuid.Parse(string(id))
	return err == nil
}

func (id EntryID) Valid() bool {
	_, err := uuid.Parse(string(id))
	return err == nil
}

// FlowKind identifies a measured cleaning-fluid movement on one ink route.
type FlowKind string

const (
	// FlowReplenish is cleaning fluid added into the route (收入).
	FlowReplenish FlowKind = "replenish"
	// FlowRecovered is fluid collected for reuse/recycling (回收).
	FlowRecovered FlowKind = "recovered"
	// FlowWaste is fluid weighed into the waste drum (废液).
	FlowWaste FlowKind = "waste"
)

func (kind FlowKind) Valid() bool {
	switch kind {
	case FlowReplenish, FlowRecovered, FlowWaste:
		return true
	default:
		return false
	}
}

// Switch is one color-change (ink-route switching) job. Every fluid movement
// that happens during the purge/clean cycle is booked against exactly one
// switch, so an abnormal loss can be traced back to that specific changeover.
type Switch struct {
	ID          SwitchID  `json:"id"`
	Circuit     string    `json:"circuit"`
	FromColor   string    `json:"from_color"`
	ToColor     string    `json:"to_color"`
	JobID       string    `json:"job_id"`
	Drained     bool      `json:"drained"`
	Opened      bool      `json:"opened"`
	OccurredAt  time.Time `json:"occurred_at"`
	RecordedAt  time.Time `json:"recorded_at"`
	Sequence    uint64    `json:"sequence"`
	AnomalyNote string    `json:"anomaly_note,omitempty"`
}

func (event Switch) Validate() error {
	if !event.ID.Valid() {
		return errors.New("wash switch id is invalid")
	}
	if strings.TrimSpace(event.Circuit) == "" {
		return errors.New("wash switch circuit is required")
	}
	if strings.TrimSpace(event.FromColor) == "" || strings.TrimSpace(event.ToColor) == "" {
		return errors.New("wash switch colors are required")
	}
	if event.FromColor == event.ToColor {
		return errors.New("wash switch from/to colors must differ")
	}
	if event.OccurredAt.IsZero() || event.RecordedAt.IsZero() {
		return errors.New("wash switch timestamps are required")
	}
	return nil
}

// Entry is one weigh-in record. Entries are append-only; a wrong reading is
// never overwritten. A correction appends a new Entry whose Supersedes points
// at the original, preserving the original value, operator and reason.
type Entry struct {
	ID         EntryID   `json:"id"`
	SwitchID   SwitchID  `json:"switch_id"`
	Circuit    string    `json:"circuit"`
	Kind       FlowKind  `json:"kind"`
	MassG      float64   `json:"mass_g"`
	Container  string    `json:"container,omitempty"`
	Operator   string    `json:"operator"`
	MeasuredAt time.Time `json:"measured_at"`
	RecordedAt time.Time `json:"recorded_at"`
	Sequence   uint64    `json:"sequence"`

	// Supersedes is non-empty only for a correction; it points at the entry
	// whose reading this one replaces.
	Supersedes EntryID `json:"supersedes,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

func (entry Entry) Validate() error {
	if !entry.ID.Valid() {
		return errors.New("wash entry id is invalid")
	}
	if !entry.SwitchID.Valid() {
		return errors.New("wash entry switch id is invalid")
	}
	if strings.TrimSpace(entry.Circuit) == "" {
		return errors.New("wash entry circuit is required")
	}
	if !entry.Kind.Valid() {
		return errors.New("wash entry kind is invalid")
	}
	if entry.MassG < 0 {
		return errors.New("wash entry mass must not be negative")
	}
	if strings.TrimSpace(entry.Operator) == "" {
		return errors.New("wash entry operator is required")
	}
	if entry.MeasuredAt.IsZero() || entry.RecordedAt.IsZero() {
		return errors.New("wash entry timestamps are required")
	}
	if (entry.Supersedes != "") != (strings.TrimSpace(entry.Reason) != "") {
		return errors.New("wash correction requires both superseded entry and reason")
	}
	if entry.Supersedes != "" && !entry.Supersedes.Valid() {
		return errors.New("wash superseded entry id is invalid")
	}
	return nil
}

// IsCorrection reports whether the entry replaces an earlier record.
func (entry Entry) IsCorrection() bool { return entry.Supersedes != "" }
