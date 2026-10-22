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
// (403/429, e.g. immowelt's bot check) the bot pauses for -block-pause.
type pace struct {
	path       string
	min, max   time.Duration
	blockPause time.Duration
}

type paceState struct {
	NextRun time.Time `json:"next_run"`
	Reason  string    `json:"reason,omitempty"`
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

// schedule saves when the next run may start. blocked lists hosts that
// refused requests this run.
func (p *pace) schedule(now time.Time, blocked []string) paceState {
	st := paceState{}
	if len(blocked) > 0 {
		st.NextRun = now.Add(p.blockPause)
		st.Reason = "refused by " + strings.Join(blocked, ", ")
	} else {
		st.NextRun = now.Add(p.min + time.Duration(rand.Int63n(int64(p.max-p.min)+1)))
	}
	os.MkdirAll(filepath.Dir(p.path), 0o755)
	b, _ := json.Marshal(st)
	os.WriteFile(p.path, b, 0o644)
	return st
}
