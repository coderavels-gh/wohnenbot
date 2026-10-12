package main

import (
	"strings"
	"testing"
	"time"

	"aptbot/internal/listing"
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
