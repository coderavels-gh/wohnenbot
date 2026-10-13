package listing

import (
	"strings"
	"testing"
)

func TestEncodeDecode(t *testing.T) {
	l := &Listing{Source: "ImmoScout24", ID: "1", Title: "T", WarmRent: 1650, SizeM2: 66, Rooms: 2,
		AvailableFrom: "01.12.2026", StrictStart: true, PlusOnly: true, Text: strings.Repeat("Balkon, Einbauküche. ", 2000),
		Checks: []Check{{Name: CritBalcony, Result: Pass}}, Verdict: Match}
	l.SetDefault(CritKitchen, Pass, "furnished")
	s := l.Encode()
	if s == "" || len(s) > 5000 {
		t.Fatalf("encoded length %d", len(s))
	}
	got, err := Decode(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.WarmRent != 1650 || got.Text != l.Text || !got.StrictStart || !got.PlusOnly || got.Defaults[CritKitchen].Result != Pass {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.Checks != nil || got.Verdict != "" {
		t.Error("checks and verdict must not be stored")
	}
	if _, err := Decode(""); err == nil {
		t.Error("empty data decoded")
	}
}
