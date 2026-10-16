// Package store remembers which listings have been processed, so each one is
// analysed, written to the tracker and alerted at most once.
package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"aptbot/internal/sheets"
)

type Record struct {
	Key       string `json:"key"`
	Source    string `json:"source"`
	Verdict   string `json:"verdict"`
	URL       string `json:"url"`
	FirstSeen string `json:"first_seen"`
	Title     string `json:"title"`
	// Fingerprint of the criteria the listing was judged under. A rejected
	// listing is evaluated again once the criteria change.
	Criteria string `json:"criteria,omitempty"`
	// Cells: readable columns (rent, m², checks, …), keyed by column name,
	// so the Seen tab shows why a listing was rejected.
	Cells map[string]any `json:"cells,omitempty"`
	// Data: the extracted listing (listing.Encode), so a rejected listing can
	// be re-evaluated after a criteria change without fetching it again.
	Data string `json:"data,omitempty"`
}

// ScanKey is the Seen key of a source's "full scan done" marker: its
// Criteria field holds the fingerprint the last full re-check ran under.
func ScanKey(source string) string { return "_scan:" + source }

// skip reports whether a recorded listing needs no new evaluation: it was
// written to the tracker (MATCH/REVIEW), or rejected under the current criteria.
func skip(r Record, criteria string) bool {
	return r.Verdict != "REJECT" || r.Criteria == criteria
}

type Store interface {
	Load(ctx context.Context) error
	Seen(key string) bool
	Count() int
	// HasSource reports whether any listing of that source was recorded.
	HasSource(source string) bool
	// NeedsRecheck reports whether a listing of that source was rejected
	// under different criteria, so its results should be read in full again.
	NeedsRecheck(source string) bool
	Add(ctx context.Context, recs []Record) error
	// Known reports whether the listing was recorded at all.
	Known(key string) bool
	// Data returns the stored listing data of a recorded listing ("" if none).
	Data(ctx context.Context, key string) string
}

// ---- local JSON file -------------------------------------------------------

type File struct {
	Path     string
	Criteria string // current criteria fingerprint
	mu       sync.Mutex
	recs     map[string]Record
}

func (f *File) Load(context.Context) error {
	f.recs = map[string]Record{}
	raw, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []Record
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	for _, r := range list {
		f.recs[r.Key] = r
	}
	return nil
}

func (f *File) Seen(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[key]
	return ok && skip(r, f.Criteria)
}
func (f *File) Count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.recs) }

func (f *File) Known(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.recs[key]
	return ok
}

func (f *File) Data(_ context.Context, key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recs[key].Data
}

func (f *File) HasSource(source string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return hasSource(f.recs, source)
}

func (f *File) NeedsRecheck(source string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return needsRecheck(f.recs, source, f.Criteria)
}

func needsRecheck(recs map[string]Record, source, criteria string) bool {
	// Once a full re-check ran under these criteria, listings still carrying
	// an older fingerprint are gone from the site; don't scan again for them.
	if m, ok := recs[ScanKey(source)]; ok && m.Criteria == criteria {
		return false
	}
	for k, r := range recs {
		if strings.HasPrefix(k, source+":") && !skip(r, criteria) {
			return true
		}
	}
	return false
}

func hasSource(recs map[string]Record, source string) bool {
	for k := range recs {
		if strings.HasPrefix(k, source+":") {
			return true
		}
	}
	return false
}

func (f *File) Add(_ context.Context, recs []Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range recs {
		f.recs[r.Key] = r
	}
	list := make([]Record, 0, len(f.recs))
	for _, r := range f.recs {
		list = append(list, r)
	}
	raw, _ := json.MarshalIndent(list, "", " ")
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// ---- Google Sheet "Seen" tab ----------------------------------------------
// Keeping state in the sheet makes the bot stateless: it can run on your Mac,
// a home server or a free CI runner and they all share the same memory.
//
// Columns are found by header name, so the tab can be reordered or styled.
// Missing columns are added at the end of the header row.

var SeenHeader = []string{
	"Key", "Source", "Verdict", "First seen", "Title", "URL", "Criteria",
	"Location", "Warm rent €", "m²", "Rooms", "Available", "Duration", "Transit",
	"Balcony", "Washer", "Kitchen", "Anmeldung", "Needs checking", "Data",
}

// Sheet keeps one row per evaluation; a re-evaluated listing gets a new row
// and the last row for a key wins.
type Sheet struct {
	Client   *sheets.Client
	Tab      string
	Criteria string // current criteria fingerprint

	mu     sync.Mutex
	header []string
	recs   map[string]Record
	data   map[string]string // loaded on first use: the column is large
}

func (s *Sheet) col(name string) int {
	for i, h := range s.header {
		if strings.TrimSpace(h) == name {
			return i
		}
	}
	return -1
}

func (s *Sheet) Load(ctx context.Context) error {
	if err := s.Client.EnsureTab(ctx, s.Tab, SeenHeader, -1, nil); err != nil {
		return err
	}
	header, err := s.Client.AddHeaderColumns(ctx, s.Tab, SeenHeader)
	if err != nil {
		return err
	}
	s.header, s.data = header, nil
	names := []string{"Key", "Source", "Verdict", "First seen", "Title", "URL", "Criteria"}
	idx := make([]int, len(names))
	for i, n := range names {
		idx[i] = s.col(n)
	}
	cols, err := s.Client.Columns(ctx, s.Tab, idx)
	if err != nil {
		return err
	}
	cell := func(c, row int) string {
		if row < len(cols[c]) {
			return cols[c][row]
		}
		return ""
	}
	s.recs = map[string]Record{}
	for row := 1; row < len(cols[0]); row++ {
		k := cell(0, row)
		if k == "" {
			continue
		}
		s.recs[k] = Record{Key: k, Source: cell(1, row), Verdict: cell(2, row), FirstSeen: cell(3, row),
			Title: cell(4, row), URL: cell(5, row), Criteria: cell(6, row)}
	}
	return nil
}

func (s *Sheet) Seen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[key]
	return ok && skip(r, s.Criteria)
}
func (s *Sheet) Count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.recs) }

func (s *Sheet) Known(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.recs[key]
	return ok
}

func (s *Sheet) Data(ctx context.Context, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]string{}
		cols, err := s.Client.Columns(ctx, s.Tab, []int{s.col("Key"), s.col("Data")})
		if err != nil {
			return "" // caller fetches the listing instead
		}
		for row := 1; row < len(cols[0]); row++ {
			if row < len(cols[1]) && cols[1][row] != "" {
				s.data[cols[0][row]] = cols[1][row] // last row wins
			}
		}
	}
	return s.data[key]
}

func (s *Sheet) NeedsRecheck(source string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return needsRecheck(s.recs, source, s.Criteria)
}

func (s *Sheet) HasSource(source string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return hasSource(s.recs, source)
}

func (s *Sheet) Add(ctx context.Context, recs []Record) error {
	rows := make([][]any, 0, len(recs))
	for _, r := range recs {
		vals := map[string]any{"Key": r.Key, "Source": r.Source, "Verdict": r.Verdict, "First seen": r.FirstSeen,
			"Title": r.Title, "URL": r.URL, "Criteria": r.Criteria, "Data": r.Data}
		for k, v := range r.Cells {
			if _, own := vals[k]; !own {
				vals[k] = v
			}
		}
		row := make([]any, len(s.header))
		for i, h := range s.header {
			if v, ok := vals[strings.TrimSpace(h)]; ok && v != "" {
				row[i] = v
			}
		}
		rows = append(rows, row)
	}
	if err := s.Client.Append(ctx, s.Tab, rows); err != nil {
		return err
	}
	s.mu.Lock()
	for _, r := range recs {
		s.recs[r.Key] = r
		if s.data != nil && r.Data != "" {
			s.data[r.Key] = r.Data
		}
	}
	s.mu.Unlock()
	return nil
}
