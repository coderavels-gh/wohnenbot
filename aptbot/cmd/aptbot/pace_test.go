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
		st := p.schedule(now, nil)
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
	st := p.schedule(now, []string{"www.immowelt.de"})
	if st.NextRun != now.Add(2*time.Hour) || p.due(now.Add(time.Hour)) {
		t.Errorf("block pause not applied: %+v", st)
	}
}
