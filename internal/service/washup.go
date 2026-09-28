package service

import (
	"fmt"
	"time"

	"github.com/wyw14/solo-038/internal/washup"
)

func (runtime *Runtime) RecordWashupEntry(circuit string, kind washup.EntryKind, quantityKG float64, switchID, barrel, operator, note string) (washup.EntryView, error) {
	if !runtime.inkCircuitKnown(circuit) {
		return washup.EntryView{}, fmt.Errorf("ink circuit %s is unknown", circuit)
	}
	entry := washup.NewEntry(circuit, kind, quantityKG, time.Now())
	entry.SwitchID = switchID
	entry.Barrel = barrel
	entry.Operator = operator
	entry.Note = note
	if err := runtime.washup.Append(entry); err != nil {
		return washup.EntryView{}, err
	}
	view, _ := runtime.washup.Entry(entry.ID)
	return view, nil
}

func (runtime *Runtime) CorrectWashupEntry(entryID string, quantityKG float64, reason string) (washup.EntryView, error) {
	if err := runtime.washup.Correct(washup.NewCorrection(entryID, quantityKG, reason, time.Now())); err != nil {
		return washup.EntryView{}, err
	}
	view, ok := runtime.washup.Entry(entryID)
	if !ok {
		return washup.EntryView{}, fmt.Errorf("washup entry %s does not exist", entryID)
	}
	return view, nil
}

func (runtime *Runtime) WashupEntries(circuit string) []washup.EntryView {
	return runtime.washup.Entries(circuit)
}

func (runtime *Runtime) WashupBalance(circuit string) washup.Report {
	return runtime.washup.Report(circuit, time.Now())
}

func (runtime *Runtime) WashupSwitches() []washup.Switch {
	return runtime.washup.Switches()
}

// recordWashupSwitch anchors a completed color changeover in the ledger so
// later refill, recovery, and waste entries can be reconciled against it.
func (runtime *Runtime) recordWashupSwitch(jobID, fromColor, toColor string) error {
	return runtime.washup.RecordSwitch(washup.NewSwitch(jobID, fromColor, toColor, time.Now()))
}

func (runtime *Runtime) inkCircuitKnown(circuit string) bool {
	for _, color := range runtime.ink.Colors() {
		if color == circuit {
			return true
		}
	}
	return false
}
