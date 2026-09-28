package wash

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func testSwitch(circuit, from, to string) Switch {
	now := time.Now().UTC()
	return Switch{
		ID:         NewSwitchID(),
		Circuit:    circuit,
		FromColor:  from,
		ToColor:    to,
		JobID:      "job-1",
		Drained:    true,
		Opened:     true,
		OccurredAt: now,
		RecordedAt: now,
	}
}

func entryFor(event Switch, kind FlowKind, mass float64) Entry {
	now := time.Now().UTC()
	return Entry{
		ID:         NewEntryID(),
		SwitchID:   event.ID,
		Circuit:    event.Circuit,
		Kind:       kind,
		MassG:      mass,
		Container:  "drum-a",
		Operator:   "zhang",
		MeasuredAt: now,
		RecordedAt: now,
	}
}

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSwitchValidation(t *testing.T) {
	valid := testSwitch("station-1", "cyan", "magenta")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid switch rejected: %v", err)
	}
	same := valid
	same.ToColor = "cyan"
	if err := same.Validate(); err == nil {
		t.Fatal("switch with identical from/to accepted")
	}
	missing := valid
	missing.Circuit = "  "
	if err := missing.Validate(); err == nil {
		t.Fatal("switch without circuit accepted")
	}
}

func TestEntryRequiresRegisteredSwitchAndCircuit(t *testing.T) {
	state := NewState()
	entry := entryFor(testSwitch("station-1", "cyan", "magenta"), FlowReplenish, 100)
	if err := state.RecordEntry(entry); err == nil {
		t.Fatal("entry booked against unregistered switch")
	}
	event := testSwitch("station-1", "cyan", "magenta")
	if err := state.RecordSwitch(event); err != nil {
		t.Fatal(err)
	}
	entry.SwitchID = event.ID
	if err := state.RecordEntry(entry); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	wrong := entryFor(event, FlowWaste, 10)
	wrong.ID = NewEntryID()
	wrong.Circuit = "station-2"
	if err := state.RecordEntry(wrong); err == nil {
		t.Fatal("entry whose circuit differs from the switch was accepted")
	}
}

func TestBalanceBalancedAndShortfall(t *testing.T) {
	state := NewState()
	event := testSwitch("station-1", "cyan", "magenta")
	if err := state.RecordSwitch(event); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		entryFor(event, FlowReplenish, 1000),
		entryFor(event, FlowRecovered, 700),
		entryFor(event, FlowWaste, 250),
	} {
		if err := state.RecordEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	// 1000 - 700 - 250 = 50 g unexplained loss; within a 60 g tolerance.
	balance, err := state.Balance(event.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !approxEqual(balance.ShortfallG, 50) {
		t.Fatalf("shortfall = %v, want 50", balance.ShortfallG)
	}
	if !balance.Balanced || len(balance.Missing) != 0 {
		t.Fatalf("50g loss within 60g tolerance should balance: %+v", balance)
	}
	// Same loss outside tolerance is an anomaly pinned to this changeover.
	ledger := state.Reconcile(40)
	if len(ledger.Anomalies) != 1 || ledger.Anomalies[0].Switch.ID != event.ID {
		t.Fatalf("expected the one shortfall switch to be flagged: %+v", ledger.Anomalies)
	}
}

func TestMultipleContainersOfSameKindAddUp(t *testing.T) {
	state := NewState()
	event := testSwitch("station-1", "cyan", "magenta")
	if err := state.RecordSwitch(event); err != nil {
		t.Fatal(err)
	}
	// Waste collected across two drums; both must count toward the balance.
	drum1 := entryFor(event, FlowWaste, 200)
	drum1.Container = "drum-a"
	drum2 := entryFor(event, FlowWaste, 100)
	drum2.Container = "drum-b"
	for _, entry := range []Entry{entryFor(event, FlowReplenish, 1000), entryFor(event, FlowRecovered, 700), drum1, drum2} {
		if err := state.RecordEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	balance, err := state.Balance(event.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !approxEqual(balance.WasteG, 300) || !balance.Balanced {
		t.Fatalf("two drums should sum to 300 and balance: %+v", balance)
	}
}

func TestBalanceFlagsMissingEvidenceAndAnomaly(t *testing.T) {
	state := NewState()
	good := testSwitch("station-1", "cyan", "magenta")
	bad := testSwitch("station-2", "magenta", "yellow")
	for _, event := range []Switch{good, bad} {
		if err := state.RecordSwitch(event); err != nil {
			t.Fatal(err)
		}
	}
	// good: fully balanced.
	for _, entry := range []Entry{
		entryFor(good, FlowReplenish, 1000),
		entryFor(good, FlowRecovered, 700),
		entryFor(good, FlowWaste, 300),
	} {
		if err := state.RecordEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	// bad: only replenish recorded; recovered+waste missing => shortfall 1000.
	if err := state.RecordEntry(entryFor(bad, FlowReplenish, 1000)); err != nil {
		t.Fatal(err)
	}
	ledger := state.Reconcile(50)
	if len(ledger.Balances) != 2 {
		t.Fatalf("expected 2 balances, got %d", len(ledger.Balances))
	}
	if len(ledger.Anomalies) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(ledger.Anomalies))
	}
	anomaly := ledger.Anomalies[0]
	if anomaly.Switch.ID != bad.ID {
		t.Fatalf("anomaly located at %s, want %s", anomaly.Switch.ID, bad.ID)
	}
	if len(anomaly.Missing) != 2 || anomaly.Missing[0] != "recovered" {
		t.Fatalf("missing kinds = %v, want [recovered waste]", anomaly.Missing)
	}
	if !approxEqual(anomaly.ShortfallG, 1000) {
		t.Fatalf("anomaly shortfall = %v, want 1000", anomaly.ShortfallG)
	}
}

func TestCorrectionPreservesOriginalAndReason(t *testing.T) {
	state := NewState()
	event := testSwitch("station-1", "cyan", "magenta")
	if err := state.RecordSwitch(event); err != nil {
		t.Fatal(err)
	}
	original := entryFor(event, FlowWaste, 250)
	if err := state.RecordEntry(original); err != nil {
		t.Fatal(err)
	}
	// Correction without a reason is rejected.
	bad := entryFor(event, FlowWaste, 290)
	bad.Supersedes = original.ID
	if err := state.RecordEntry(bad); err == nil {
		t.Fatal("correction without reason accepted")
	}
	correction := entryFor(event, FlowWaste, 290)
	correction.Supersedes = original.ID
	correction.Reason = "scale was not tared; re-weighed after drum swap"
	if err := state.RecordEntry(correction); err != nil {
		t.Fatalf("valid correction rejected: %v", err)
	}
	// Original record still readable, unchanged.
	kept, ok := state.Entry(original.ID)
	if !ok {
		t.Fatal("original entry disappeared after correction")
	}
	if !approxEqual(kept.MassG, 250) || kept.Reason != "" {
		t.Fatalf("original entry mutated: %+v", kept)
	}
	// Effective balance uses the corrected 290 value.
	balance, err := state.Balance(event.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !approxEqual(balance.WasteG, 290) {
		t.Fatalf("effective waste = %v, want 290", balance.WasteG)
	}
	// Chain exposes original -> correction with the reason.
	chain := state.CorrectionChain(event.ID, FlowWaste)
	if len(chain) != 2 || chain[0].ID != original.ID || chain[1].ID != correction.ID {
		t.Fatalf("correction chain = %+v", chain)
	}
	if chain[1].Reason != correction.Reason {
		t.Fatal("correction reason not retained")
	}
	// The original cannot be corrected a second time.
	second := entryFor(event, FlowWaste, 295)
	second.Supersedes = original.ID
	second.Reason = "again"
	if err := state.RecordEntry(second); err == nil {
		t.Fatal("superseded entry corrected a second time")
	}
	// A correction cannot switch flow kind.
	wrongKind := entryFor(event, FlowRecovered, 1)
	wrongKind.Supersedes = correction.ID
	wrongKind.Reason = "cross-kind"
	if err := state.RecordEntry(wrongKind); err == nil {
		t.Fatal("correction across flow kinds accepted")
	}
}

func TestStoreReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wash-ledger.jsonl")
	store := NewStore(path)
	event := testSwitch("station-1", "cyan", "magenta")
	if _, err := store.RecordSwitch(&event); err != nil {
		t.Fatal(err)
	}
	original := entryFor(event, FlowReplenish, 1000)
	if _, err := store.RecordEntry(&original); err != nil {
		t.Fatal(err)
	}
	correction := entryFor(event, FlowReplenish, 980)
	correction.Supersedes = original.ID
	correction.Reason = "double-counted first pour"
	if _, err := store.RecordEntry(&correction); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		entryFor(event, FlowRecovered, 700),
		entryFor(event, FlowWaste, 280),
	} {
		if _, err := store.RecordEntry(&entry); err != nil {
			t.Fatal(err)
		}
	}

	// A fresh store over the same file must reconstruct the identical view.
	reloaded := NewStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	switches := reloaded.State().Switches()
	if len(switches) != 1 || switches[0].ID != event.ID {
		t.Fatalf("reloaded switches = %+v", switches)
	}
	if switches[0].Sequence != 1 {
		t.Fatalf("reloaded sequence = %d, want 1", switches[0].Sequence)
	}
	raw := reloaded.State().Entries(event.ID)
	if len(raw) != 4 {
		t.Fatalf("expected 4 raw entries (original retained), got %d", len(raw))
	}
	balance, err := reloaded.State().Balance(event.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Effective replenish is the corrected 980.
	if !approxEqual(balance.ReplenishG, 980) || !approxEqual(balance.RecoveredG, 700) || !approxEqual(balance.WasteG, 280) {
		t.Fatalf("reloaded balance = %+v", balance)
	}
	if !balance.Balanced {
		t.Fatalf("980 - 700 - 280 = 0 should balance: %+v", balance)
	}
}

func TestStoreDurabilityFailureLeavesCleanState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wash-ledger.jsonl")
	store := NewStore(path)
	event := testSwitch("station-1", "cyan", "magenta")
	store.FailNext(sentinelFailure{msg: "injected disk fault"})
	if _, err := store.RecordSwitch(&event); err == nil {
		t.Fatal("expected injected append failure")
	}
	if _, ok := store.State().Switch(event.ID); ok {
		t.Fatal("switch appeared in memory despite failed durable write")
	}
	// Subsequent append works.
	if _, err := store.RecordSwitch(&event); err != nil {
		t.Fatalf("append after failure errored: %v", err)
	}
	// Only the successful switch is on disk.
	reloaded := NewStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if switches := reloaded.State().Switches(); len(switches) != 1 || switches[0].Sequence != 1 {
		t.Fatalf("replayed ledger = %+v, want exactly one switch at sequence 1", switches)
	}
}

type sentinelFailure struct{ msg string }

func (err sentinelFailure) Error() string { return err.msg }
