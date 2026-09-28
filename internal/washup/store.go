package washup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type recordKind string

const (
	recordEntry      recordKind = "entry"
	recordCorrection recordKind = "correction"
	recordSwitch     recordKind = "switch"
)

// record is the on-disk envelope: one JSON line per ledger fact so that
// corrections and switches share the same append-only file as entries.
type record struct {
	RecordKind recordKind  `json:"record_kind"`
	Entry      *Entry      `json:"entry,omitempty"`
	Correction *Correction `json:"correction,omitempty"`
	Switch     *Switch     `json:"switch,omitempty"`
}

func (rec record) validate() error {
	switch rec.RecordKind {
	case recordEntry:
		if rec.Entry == nil {
			return errors.New("washup record is missing its entry")
		}
		return rec.Entry.Validate()
	case recordCorrection:
		if rec.Correction == nil {
			return errors.New("washup record is missing its correction")
		}
		return rec.Correction.Validate()
	case recordSwitch:
		if rec.Switch == nil {
			return errors.New("washup record is missing its switch")
		}
		return rec.Switch.Validate()
	default:
		return fmt.Errorf("washup record kind %q is unsupported", rec.RecordKind)
	}
}

type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (store *Store) Append(rec record) error {
	if err := rec.validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(store.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	if err = encoder.Encode(rec); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("append washup ledger record: %w", err)
	}
	return nil
}

func (store *Store) Records() ([]record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	records := []record{}
	for scanner.Scan() {
		var rec record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, err
		}
		if err := rec.validate(); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, scanner.Err()
}
