// Package sources implements one scraper per listing site.
package sources

import (
	"context"
	"log"
	"regexp"
	"strings"

	"aptbot/internal/listing"
)

// Source discovers listings and fetches details for the ones not seen yet.
type Source interface {
	Name() string
	// Fetch passes each listing whose key (Source+":"+ID) is not seen to emit
	// as soon as its details are fetched.
	Fetch(ctx context.Context, seen func(key string) bool, emit func(*listing.Listing)) error
}

// furnishedAgencyDefaults: these agencies only list fully furnished flats.
func furnishedAgencyDefaults(l *listing.Listing, site string) {
	l.Furnished = true
	l.SetDefault(listing.CritKitchen, listing.Pass, "fully furnished "+site+" flat (kitchen included)")
}

var reMultiNL = regexp.MustCompile(`\n{2,}`)

func cleanText(s string) string {
	return strings.TrimSpace(reMultiNL.ReplaceAllString(s, "\n"))
}

func logf(src, format string, args ...any) {
	log.Printf("["+src+"] "+format, args...)
}
