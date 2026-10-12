// Package store remembers which listings have been processed, so each one is
// analysed, written to the tracker and alerted at most once.
package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
}

type Store interface {
	Load(ctx context.Context) error
	Seen(key string) bool
	Count() int
	Add(ctx context.Context, recs []Record) error
}

// ---- local JSON file -------------------------------------------------------

type File struct {
	Path string
	mu   sync.Mutex
	recs map[string]Record
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
	_, ok := f.recs[key]
	return ok
}
func (f *File) Count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.recs) }

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

var SeenHeader = []string{"Key", "Source", "Verdict", "First seen", "Title", "URL"}

type Sheet struct {
	Client *sheets.Client
	Tab    string
	mu     sync.Mutex
	keys   map[string]bool
}

func (s *Sheet) Load(ctx context.Context) error {
	if err := s.Client.EnsureTab(ctx, s.Tab, SeenHeader, -1, nil); err != nil {
		return err
	}
	col, err := s.Client.Column(ctx, s.Tab, "A")
	if err != nil {
		return err
	}
	s.keys = map[string]bool{}
	for i, k := range col {
		if i == 0 && k == "Key" {
			continue
		}
		s.keys[k] = true
	}
	return nil
}

func (s *Sheet) Seen(key string) bool { s.mu.Lock(); defer s.mu.Unlock(); return s.keys[key] }
func (s *Sheet) Count() int           { s.mu.Lock(); defer s.mu.Unlock(); return len(s.keys) }

func (s *Sheet) Add(ctx context.Context, recs []Record) error {
	rows := make([][]any, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, []any{r.Key, r.Source, r.Verdict, r.FirstSeen, r.Title, r.URL})
	}
	if err := s.Client.Append(ctx, s.Tab, rows); err != nil {
		return err
	}
	s.mu.Lock()
	for _, r := range recs {
		s.keys[r.Key] = true
	}
	s.mu.Unlock()
	return nil
}
