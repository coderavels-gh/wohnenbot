// stops extracts every bus stop, U/S-Bahn station and ferry pier in and around
// Hamburg from OpenStreetMap (one Overpass query) into the bot's local stop
// list, internal/analyze/hamburg_stops.json.gz. Run it from a machine the
// Overpass servers accept (the "refresh stops" GitHub workflow does).
//
//	go run ./cmd/stops -out internal/analyze/hamburg_stops.json.gz
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"time"

	"aptbot/internal/analyze"
	"aptbot/internal/web"
)

// Hamburg plus ~2 km, so flats near the border see stops just outside it.
var bbox = [4]float64{53.37, 9.70, 53.78, 10.36}

func main() {
	out := flag.String("out", "internal/analyze/hamburg_stops.json.gz", "output file")
	server := flag.String("server", "https://overpass-api.de/api/interpreter", "Overpass server")
	contact := flag.String("contact", "", "contact e-mail for the User-Agent")
	flag.Parse()

	b := fmt.Sprintf("%f,%f,%f,%f", bbox[0], bbox[1], bbox[2], bbox[3])
	q := fmt.Sprintf(`[out:json][timeout:180];(
node(%[1]s)[highway=bus_stop];
node(%[1]s)[public_transport=platform][bus=yes];
node(%[1]s)[railway~"^(station|halt|stop)$"];
node(%[1]s)[public_transport=station];
node(%[1]s)[amenity=ferry_terminal];
node(%[1]s)[public_transport=platform][ferry=yes];
);out;`, b)
	client := web.NewClient(time.Second)
	host, _ := url.Parse(*server)
	client.HostTimeout[host.Host] = 5 * time.Minute
	client.HostAttempts[host.Host] = 2
	ua := "aptbot-hamburg/1.0 (personal flat search; stop list refresh)"
	if *contact != "" {
		ua += " " + *contact
	}
	body, err := client.Do(context.Background(), "POST", *server, []byte("data="+url.QueryEscape(q)),
		map[string]string{"User-Agent": ua, "Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json"})
	if err != nil {
		log.Fatal(err)
	}
	var resp struct {
		Elements []struct {
			Lat  float64           `json:"lat"`
			Lon  float64           `json:"lon"`
			Tags map[string]string `json:"tags"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Fatalf("overpass answer: %v", err)
	}
	f := analyze.StopsFile{
		Attribution: "Stop data © OpenStreetMap contributors, ODbL 1.0 (https://www.openstreetmap.org/copyright)",
		Generated:   time.Now().UTC().Format(time.RFC3339),
		BBox:        bbox,
	}
	count := map[string]int{}
	for _, e := range resp.Elements {
		modes := analyze.ClassifyStopTags(e.Tags)
		if len(modes) == 0 {
			continue
		}
		sort.Strings(modes)
		for _, m := range modes {
			count[m]++
		}
		f.Stops = append(f.Stops, analyze.Stop{Lat: round6(e.Lat), Lon: round6(e.Lon), Modes: modes, Name: e.Tags["name"]})
	}
	if len(f.Stops) < 1000 {
		log.Fatalf("only %d stops: refusing to write a suspiciously small list", len(f.Stops))
	}
	raw, _ := json.Marshal(f)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(raw)
	zw.Close()
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s: %d stops %v (%d KB)", *out, len(f.Stops), count, buf.Len()/1024)
}

func round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }
