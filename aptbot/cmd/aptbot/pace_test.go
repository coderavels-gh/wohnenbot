package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPace(t *testing.T) {
	if _, _, err := parsePace("30m-15m"); err == nil {
		t.Error("max < min accepted")
	}
	lo, hi, err := parsePace("15m-30m")
	if err != nil || lo != 15*time.Minute || hi != 30*time.Minute {
		t.Fatalf("parsePace: %v %v %v", lo, hi, err)
	}
	p := &pace{path: filepath.Join(t.TempDir(), "pace.json"), min: lo, max: hi, blockPause: 2 * time.Hour}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if !p.due(now) {
		t.Fatal("first run should be due")
	}
	for i := 0; i < 50; i++ {
		st := p.schedule(now, nil, nil)
		if gap := st.NextRun.Sub(now); gap < lo || gap > hi {
			t.Fatalf("gap %v outside 15–30 min", gap)
		}
	}
	if p.due(now.Add(14 * time.Minute)) {
		t.Error("due before the minimum gap")
	}
	if !p.due(now.Add(30 * time.Minute)) {
		t.Error("not due after the maximum gap")
	}
	// A refusal pauses that host only; the next run keeps the normal gap.
	st := p.schedule(now, []string{"www.immowelt.de"}, []string{"Immowelt", "Kleinanzeigen"})
	if st.Paused["www.immowelt.de"] != now.Add(2*time.Hour) || !p.due(now.Add(30*time.Minute)) {
		t.Errorf("block pause not applied per host: %+v", st)
	}
	// Later runs keep the pause until it ends, and add new ones.
	st = p.schedule(now.Add(time.Hour), []string{"www.kleinanzeigen.de"}, []string{"Immowelt"})
	if len(st.Paused) != 2 || st.Paused["www.immowelt.de"] != now.Add(2*time.Hour) {
		t.Errorf("pauses not carried over: %+v", st)
	}
	if got := p.paused(now.Add(150 * time.Minute)); len(got) != 1 || got["www.kleinanzeigen.de"].IsZero() {
		t.Errorf("expired pause still listed: %v", got)
	}
	// Kleinanzeigen ran at now, Immowelt an hour later: a 2-hour source is
	// due again only 2 hours after its own last run.
	if p.sourceDue("Kleinanzeigen", 2*time.Hour, now.Add(119*time.Minute)) || !p.sourceDue("Kleinanzeigen", 2*time.Hour, now.Add(2*time.Hour)) {
		t.Error("Kleinanzeigen interval not kept")
	}
	if !p.sourceDue("ImmoScout", 2*time.Hour, now) {
		t.Error("a source that never ran should be due")
	}
}
