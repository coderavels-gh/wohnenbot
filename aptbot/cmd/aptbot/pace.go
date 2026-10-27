package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pace spaces out -once runs that launchd starts every minute (the Mac
// LaunchAgent): a run only goes ahead once the time saved by the previous one
// has passed. After a normal run the next one is a random 15–30 minutes away
// (-pace), so requests don't follow a fixed clock; after a site refused us
// (403/429, e.g. immowelt's bot check) that site alone is left alone for
// -block-pause; the other sites keep their normal pace.
type pace struct {
	path       string
	min, max   time.Duration
	blockPause time.Duration
}

type paceState struct {
	NextRun time.Time `json:"next_run"`
	// Paused: host → until when it isn't contacted (it refused us).
	Paused map[string]time.Time `json:"paused,omitempty"`
	// LastRun: source → when it last ran, for sources checked less often.
	LastRun map[string]time.Time `json:"last_run,omitempty"`
}

// parsePace reads "15m-30m" (or a single duration).
func parsePace(s string) (min, max time.Duration, err error) {
	lo, hi, found := strings.Cut(s, "-")
	if min, err = time.ParseDuration(lo); err != nil {
		return 0, 0, fmt.Errorf("-pace %q: %w", s, err)
	}
	max = min
	if found {
		if max, err = time.ParseDuration(hi); err != nil {
			return 0, 0, fmt.Errorf("-pace %q: %w", s, err)
		}
	}
	if min <= 0 || max < min {
		return 0, 0, fmt.Errorf("-pace %q: want min-max with 0 < min ≤ max", s)
	}
	return min, max, nil
}

func (p *pace) load() paceState {
	var st paceState
	if b, err := os.ReadFile(p.path); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// due reports whether a run may start now.
func (p *pace) due(now time.Time) bool {
	return !now.Before(p.load().NextRun)
}

// paused returns the hosts still paused at now, with their end times.
func (p *pace) paused(now time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	for h, until := range p.load().Paused {
		if now.Before(until) {
			out[h] = until
		}
	}
	return out
}

// sourceDue reports whether a source checked at most every interval may run.
func (p *pace) sourceDue(name string, every time.Duration, now time.Time) bool {
	return now.Sub(p.load().LastRun[name]) >= every
}

// schedule saves when the next run may start, which hosts stay paused and
// when each source last ran. blocked lists hosts that refused requests this
// run: each is paused for blockPause, on top of the pauses still running.
// ran lists the sources this run checked.
func (p *pace) schedule(now time.Time, blocked, ran []string) paceState {
	old := p.load()
	st := paceState{Paused: p.paused(now), LastRun: map[string]time.Time{}}
	for _, h := range blocked {
		st.Paused[h] = now.Add(p.blockPause)
	}
	for name, t := range old.LastRun {
		st.LastRun[name] = t
	}
	for _, name := range ran {
		st.LastRun[name] = now
	}
	st.NextRun = now.Add(p.min + time.Duration(rand.Int63n(int64(p.max-p.min)+1)))
	os.MkdirAll(filepath.Dir(p.path), 0o755)
	b, _ := json.Marshal(st)
	os.WriteFile(p.path, b, 0o644)
	return st
}
