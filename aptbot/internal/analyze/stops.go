package analyze

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"math"
	"sync"
)

// The local stop list: every bus stop, U/S-Bahn station and ferry pier in and
// around Hamburg, extracted once from OpenStreetMap by cmd/stops (refresh with
// the "refresh stops" workflow). With it, the transit check needs no network
// at all. Stop data © OpenStreetMap contributors, ODbL.
//
//go:embed hamburg_stops.json.gz
var embeddedStops []byte

// StopsFile is the format written by cmd/stops.
type StopsFile struct {
	Attribution string     `json:"attribution"`
	Generated   string     `json:"generated"`
	BBox        [4]float64 `json:"bbox"` // south, west, north, east
	Stops       []Stop     `json:"stops"`
}

// Stop is one stop with the modes it serves.
type Stop struct {
	Lat   float64  `json:"la"`
	Lon   float64  `json:"lo"`
	Modes []string `json:"m"`
	Name  string   `json:"n,omitempty"`
}

var (
	localOnce  sync.Once
	localStops *StopsFile
)

// LocalStops returns the embedded stop list, or nil if it is empty/unreadable.
func LocalStops() *StopsFile {
	localOnce.Do(func() {
		if len(embeddedStops) == 0 {
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(embeddedStops))
		if err != nil {
			log.Printf("transit: local stop list: %v", err)
			return
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			log.Printf("transit: local stop list: %v", err)
			return
		}
		var f StopsFile
		if err := json.Unmarshal(raw, &f); err != nil || len(f.Stops) == 0 {
			log.Printf("transit: local stop list unusable: %v", err)
			return
		}
		localStops = &f
	})
	return localStops
}

// covers reports whether a point lies inside the list's bounding box.
func (f *StopsFile) covers(lat, lon float64) bool {
	return lat >= f.BBox[0] && lat <= f.BBox[2] && lon >= f.BBox[1] && lon <= f.BBox[3]
}

// near returns the modes with a stop within reach: bus stops within busR,
// U/S-Bahn and ferry within railR (metres). The example per mode is the
// nearest stop's name.
func (f *StopsFile) near(lat, lon float64, busR, railR int) map[string]string {
	best := map[string]float64{}
	out := map[string]string{}
	for _, s := range f.Stops {
		d := distanceM(lat, lon, s.Lat, s.Lon)
		for _, m := range s.Modes {
			r := float64(railR)
			if m == ModeBus {
				r = float64(busR)
			}
			if d > r {
				continue
			}
			if b, ok := best[m]; !ok || d < b {
				best[m] = d
				out[m] = s.Name
			}
		}
	}
	return out
}

// distanceM is the great-circle distance in metres.
func distanceM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// ClassifyStopTags exposes the stop classification for cmd/stops: the modes a
// tagged OSM node serves, or none if it isn't in service.
func ClassifyStopTags(tags map[string]string) []string {
	if notInService(tags) {
		return nil
	}
	return classifyStop(tags)
}
