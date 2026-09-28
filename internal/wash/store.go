package wash

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// record envelopes every persisted ledger line so switches and entries can
// share one append-only JSONL file and be replayed in order.
type record struct {
	Type   string  `json:"type"`
	Switch *Switch `json:"switch,omitempty"`
	Entry  *Entry  `json:"entry,omitempty"`
}

// Store is an append-only, fsynced JSONL ledger for wash switches and weigh-in
// entries. It mirrors journal.Store: every Append is durably synced before the
// in-memory sequence advances, and Load replays the file back into a State so
// corrections and original records both survive a restart.
type Store struct {
	mu       sync.Mutex
	path     string
	state    *State
	sequence uint64
	failNext error
}

func NewStore(path string) *Store {
	return &Store{path: path, state: NewState()}
}

// State returns the in-memory ledger the store appends into.
func (store *Store) State() *State { return store.state }

// FailNext instructs the next append to fail; it is used to exercise the
// durability error path.
func (store *Store) FailNext(err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failNext = err
}

func (store *Store) appendRecord(line record) (uint64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return 0, err
	}
	if store.failNext != nil {
		err := store.failNext
		store.failNext = nil
		return 0, err
	}
	// Issue the append sequence before marshalling so the durable line and the
	// in-memory record carry the same number. A failed write leaves a gap, the
	// same trade-off journal.Store makes with its issued/durable counters.
	store.sequence++
	sequence := store.sequence
	if line.Switch != nil {
		line.Switch.Sequence = sequence
	}
	if line.Entry != nil {
		line.Entry.Sequence = sequence
	}
	file, err := os.OpenFile(store.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(line)
	if err != nil {
		_ = file.Close()
		return 0, err
	}
	if _, err = file.Write(append(encoded, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, fmt.Errorf("append wash ledger record: %w", err)
	}
	return sequence, nil
}

// RecordSwitch durably appends a switch and applies it to the in-memory state.
// The durable write happens first; the in-memory ledger is updated only after
// the fsync succeeds, so a failed write never presents a phantom changeover.
func (store *Store) RecordSwitch(event *Switch) (uint64, error) {
	if err := event.Validate(); err != nil {
		return 0, err
	}
	store.mu.Lock()
	exists := store.state.hasSwitch(event.ID)
	store.mu.Unlock()
	if exists {
		return 0, fmt.Errorf("wash switch %s already recorded", event.ID)
	}
	sequence, err := store.appendRecord(record{Type: "switch", Switch: event})
	if err != nil {
		return 0, err
	}
	event.Sequence = sequence
	if err := store.state.RecordSwitch(*event); err != nil {
		return 0, err
	}
	return sequence, nil
}

// RecordEntry durably appends a weigh-in entry (or correction) and applies it
// in memory. Validation against the ledger (switch present, circuit match,
// correction target valid) runs before the durable write. The assigned append
// sequence is written back onto entry before returning.
func (store *Store) RecordEntry(entry *Entry) (uint64, error) {
	if err := entry.Validate(); err != nil {
		return 0, err
	}
	if err := store.state.previewEntry(*entry); err != nil {
		return 0, err
	}
	sequence, err := store.appendRecord(record{Type: "entry", Entry: entry})
	if err != nil {
		return 0, err
	}
	entry.Sequence = sequence
	if err := store.state.RecordEntry(*entry); err != nil {
		return 0, err
	}
	return sequence, nil
}

// Load replays the ledger file into a fresh state, preserving append order and
// sequences so reconciliation after restart is identical to before.
func (store *Store) Load() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		store.state = NewState()
		store.sequence = 0
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	state := NewState()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var sequence uint64
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var line record
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return fmt.Errorf("decode wash ledger record: %w", err)
		}
		sequence++
		switch line.Type {
		case "switch":
			if line.Switch == nil {
				return fmt.Errorf("wash ledger switch record is empty")
			}
			line.Switch.Sequence = sequence
			if err := state.RecordSwitch(*line.Switch); err != nil {
				return err
			}
		case "entry":
			if line.Entry == nil {
				return fmt.Errorf("wash ledger entry record is empty")
			}
			line.Entry.Sequence = sequence
			if err := state.RecordEntry(*line.Entry); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown wash ledger record type %q", line.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	store.state = state
	store.sequence = sequence
	return nil
}
