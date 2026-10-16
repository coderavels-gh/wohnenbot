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
		{Key: "X:a", Verdict: "REJECT", Criteria: "old"},
		{Key: "X:b", Verdict: "REVIEW", Criteria: "old"},
		{Key: "X:c", Verdict: "MATCH", Criteria: "old"},
	}); err != nil {
		t.Fatal(err)
	}

	same := &File{Path: path, Criteria: "old"}
	if err := same.Load(ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"X:a", "X:b", "X:c"} {
		if !same.Seen(k) {
			t.Errorf("same criteria: %s should be skipped", k)
		}
	}

	changed := &File{Path: path, Criteria: "new"}
	if err := changed.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if changed.Seen("X:a") {
		t.Error("rejected listing should be re-evaluated after the criteria change")
	}
	if !changed.Seen("X:b") || !changed.Seen("X:c") {
		t.Error("listings already in the tracker should not be re-evaluated")
	}
	if changed.Seen("unknown") {
		t.Error("unknown key reported as seen")
	}
	if same.NeedsRecheck("X") || !changed.NeedsRecheck("X") || changed.NeedsRecheck("Y") {
		t.Error("NeedsRecheck should be true only after the criteria change")
	}
	// After a full scan under the new criteria, leftover old rejects (gone
	// from the site) don't trigger another one.
	changed.Add(ctx, []Record{{Key: ScanKey("X"), Source: "X", Verdict: "SCAN", Criteria: "new"}})
	if changed.NeedsRecheck("X") {
		t.Error("NeedsRecheck still true after the full scan marker")
	}
}
