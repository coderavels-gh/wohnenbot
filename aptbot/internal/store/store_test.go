package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRejectedReevaluatedWhenCriteriaChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seen.json")
	f := &File{Path: path, Criteria: "old"}
	if err := f.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(ctx, []Record{
		{Key: "a", Verdict: "REJECT", Criteria: "old"},
		{Key: "b", Verdict: "REVIEW", Criteria: "old"},
		{Key: "c", Verdict: "MATCH", Criteria: "old"},
	}); err != nil {
		t.Fatal(err)
	}

	same := &File{Path: path, Criteria: "old"}
	if err := same.Load(ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if !same.Seen(k) {
			t.Errorf("same criteria: %s should be skipped", k)
		}
	}

	changed := &File{Path: path, Criteria: "new"}
	if err := changed.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if changed.Seen("a") {
		t.Error("rejected listing should be re-evaluated after the criteria change")
	}
	if !changed.Seen("b") || !changed.Seen("c") {
		t.Error("listings already in the tracker should not be re-evaluated")
	}
	if changed.Seen("unknown") {
		t.Error("unknown key reported as seen")
	}
}
