package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/notify"
	"aptbot/internal/sources"
	"aptbot/internal/store"
)

func TestAlertAndRow(t *testing.T) {
	l := &listing.Listing{Source: "ImmoScout24", ID: "1", URL: "https://www.immobilienscout24.de/expose/1",
		Title: "Helle 2,5-Zi mit Südbalkon", WarmRent: 1480, SizeM2: 62, Rooms: 2.5, District: "Hoheluft-Ost",
		Verdict: listing.Review, FirstSeen: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC),
		Checks: []listing.Check{{Name: listing.CritTransit, Result: listing.Pass, Detail: "Bus, U-Bahn (OSM)"}, {Name: listing.CritWasher, Detail: "not mentioned"}}}
	m := alertMessage(l)
	t.Log("\n" + m.Body)
	for _, want := range []string{"1.480 € warm", "62 m²", "2.5 Zi", "Hoheluft-Ost", "unconfirmed: washer"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("alert missing %q", want)
		}
	}
	vals := sheetRow(l)
	for _, h := range listingsHeader {
		if _, ok := vals[h]; !ok && h != "Notes" {
			t.Errorf("no value for column %q", h)
		}
	}
}

func TestOrderRowFollowsSheetHeader(t *testing.T) {
	// The tracker as formatted in the sheet: reordered, with a formula column.
	header := []string{"Status", "Verdict", "Title", "Location", "Warm rent €", "m²", "€/m²", "Rooms", "Notes", "Key", "My own column"}
	row := orderRow(header, map[string]any{"Status": "New", "Verdict": "REVIEW", "Title": "T", "Location": "",
		"Warm rent €": 1800.0, "m²": 75.0, "Rooms": 3.0, "Key": "City-Wohnen:1"})
	want := []any{"New", "REVIEW", "T", nil, 1800.0, 75.0, nil, 3.0, nil, "City-Wohnen:1", nil}
	if len(row) != len(want) {
		t.Fatalf("len %d want %d", len(row), len(want))
	}
	for i := range want {
		if row[i] != want[i] {
			t.Errorf("%s: %v want %v", header[i], row[i], want[i])
		}
	}
}

func TestPlusOnlyMarked(t *testing.T) {
	l := &listing.Listing{Source: "ImmoScout24", ID: "2", URL: "https://www.immobilienscout24.de/expose/2",
		Title: "Möblierte 2-Zimmer-Wohnung", PlusOnly: true, Verdict: listing.Match}
	m := alertMessage(l)
	if !strings.Contains(m.Body, "⭐ Plus · Möblierte") || !strings.Contains(m.Body, "Plus-only") || !strings.HasPrefix(m.Title, "[Plus] ") {
		t.Errorf("alert not marked: %q / %q", m.Title, m.Body)
	}
	if got := sheetRow(l)["Title"]; got != "⭐ Plus · Möblierte 2-Zimmer-Wohnung" {
		t.Errorf("sheet title %q", got)
	}
}

// fakeSource lists a fixed set of IDs and counts detail fetches.
type fakeSource struct {
	ids     []string
	fetched int
}

func (f *fakeSource) Name() string { return "Fake" }
func (f *fakeSource) Fetch(_ context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	for _, id := range f.ids {
		if seen("Fake:" + id) {
			continue
		}
		f.fetched++
		emit(&listing.Listing{Source: "Fake", ID: id, WarmRent: 1750})
	}
	return nil
}

func TestRejectedRevivedFromStoredData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seen.json")
	stored := &listing.Listing{Source: "Fake", ID: "1", WarmRent: 1750}
	st := &store.File{Path: path, Criteria: "old"}
	st.Load(ctx)
	st.Add(ctx, []store.Record{
		{Key: "Fake:1", Source: "Fake", Verdict: "REJECT", Criteria: "old", Data: stored.Encode()}, // still online
		{Key: "Fake:2", Source: "Fake", Verdict: "REJECT", Criteria: "old"},                        // online, no data
		{Key: "Fake:3", Source: "Fake", Verdict: "REJECT", Criteria: "old", Data: stored.Encode()}, // gone
	})

	st2 := &store.File{Path: path, Criteria: "new"}
	st2.Load(ctx)
	a := &app{store: st2}
	src := &fakeSource{ids: []string{"1", "2", "9"}} // 9 = brand new
	var got []string
	revived, err := a.fetch(ctx, src, func(l *listing.Listing) { got = append(got, l.Key()) })
	if err != nil {
		t.Fatal(err)
	}
	if revived != 1 {
		t.Fatalf("revived %d", revived)
	}
	// 1 revived from stored data (no fetch), 2 fetched (no data), 9 fetched (new); 3 is gone.
	if src.fetched != 2 || strings.Join(got, ",") != "Fake:1,Fake:2,Fake:9" {
		t.Fatalf("fetched %d, emitted %v", src.fetched, got)
	}
}

// TestRunFlushesDuringScan: with a file store and no sheet, a run records
// listings in batches as the source emits them.
func TestRunFlushesDuringScan(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seen.json")
	var ids []string
	for i := 0; i < 25; i++ {
		ids = append(ids, fmt.Sprint(i))
	}
	src := &probeSource{fakeSource: fakeSource{ids: ids}, path: path}
	a := &app{cfg: &config.Config{}, store: &store.File{Path: path, Criteria: "c"}, sources: []sources.Source{src},
		eval: &analyze.Evaluator{}, notify: notify.NewDispatcher(config.NotifyConfig{}), noAlert: true, loc: time.UTC}
	if err := a.run(ctx); err != nil {
		t.Fatal(err)
	}
	if src.recordedMidScan < flushEvery {
		t.Fatalf("only %d listings recorded while the source was still running", src.recordedMidScan)
	}
	st := &store.File{Path: path}
	st.Load(ctx)
	if st.Count() != 25 {
		t.Fatalf("recorded %d, want 25", st.Count())
	}
}

// probeSource checks the state file just before emitting its last listing.
type probeSource struct {
	fakeSource
	path            string
	recordedMidScan int
}

func (p *probeSource) Fetch(ctx context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	for i, id := range p.ids {
		if i == len(p.ids)-1 {
			st := &store.File{Path: p.path}
			st.Load(ctx)
			p.recordedMidScan = st.Count()
		}
		if !seen("Fake:" + id) {
			emit(&listing.Listing{Source: "Fake", ID: id})
		}
	}
	return nil
}

// While OSM is down, a flat with an exact location is recorded as DEFERRED
// (no tracker row); once OSM works it is re-checked from its stored Data
// without the source being asked again.
func TestDeferredWhenOSMDown(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seen.json")
	cr := config.Criteria{MinTransitModes: 2, CommuteHbf: true, RailRadiusM: 1000, BusRadiusM: 1000}
	run := func(tc *analyze.TransitChecker, src sources.Source) {
		a := &app{cfg: &config.Config{}, store: &store.File{Path: path, Criteria: "c"}, sources: []sources.Source{src},
			eval: &analyze.Evaluator{Criteria: cr, Transit: tc}, notify: notify.NewDispatcher(config.NotifyConfig{}),
			noAlert: true, loc: time.UTC}
		if err := a.run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	run(analyze.NewDownTransitChecker(), deferSource{})
	st := &store.File{Path: path}
	st.Load(ctx)
	if got := st.Deferred(); len(got) != 1 || got[0] != "Fake:exact" {
		t.Fatalf("deferred %v, want [Fake:exact]", got)
	}
	if !st.Known("Fake:vague") {
		t.Error("flat without exact location should be judged from its text and recorded")
	}

	// OSM back (offline checker: text-based, not "down"); the source lists nothing.
	tc := analyze.NewTransitChecker(nil, "")
	tc.Offline = true
	run(tc, emptySource{})
	st.Load(ctx)
	if len(st.Deferred()) != 0 {
		t.Errorf("still deferred after OSM came back: %v", st.Deferred())
	}
}

type emptySource struct{}

func (emptySource) Name() string { return "Fake" }
func (emptySource) Fetch(context.Context, func(string) bool, func(*listing.Listing)) error {
	return nil
}

type deferSource struct{}

func (deferSource) Name() string { return "Fake" }
func (deferSource) Fetch(_ context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	// Berlin: outside the built-in Hamburg stop list, so OSM is needed.
	emit(&listing.Listing{Source: "Fake", ID: "exact", Lat: 52.52, Lon: 13.40, Text: "U-Bahn und Bus"})
	emit(&listing.Listing{Source: "Fake", ID: "vague", Text: "U-Bahn und Bus"})
	return nil
}
