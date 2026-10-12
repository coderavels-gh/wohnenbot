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
	if row := sheetRow(l); len(row) != len(listingsHeader) {
		t.Fatalf("row has %d cols, header %d", len(row), len(listingsHeader))
	}
}
