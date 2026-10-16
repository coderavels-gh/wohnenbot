package web

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestLZDecompressVectors(t *testing.T) {
	for _, v := range lzVectors {
		got, err := LZDecompressFromBase64(v.in)
		if err != nil || got != v.out {
			t.Errorf("%q → %q, %v; want %q", v.in, got, err, v.out)
		}
	}
}

// The real immowelt search page: the embedded blob decodes to JSON.
func TestLZDecompressImmowelt(t *testing.T) {
	b, err := os.ReadFile("../sources/testdata/immowelt_search.html")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)window\["__UFRN_FETCHER__"\]=JSON\.parse\((".*?")\);?\s*</script>`).FindSubmatch(b)
	var inner string
	if m == nil || json.Unmarshal(m[1], &inner) != nil {
		t.Fatal("no fetcher data")
	}
	var d struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	json.Unmarshal([]byte(inner), &d)
	var blob string
	for k, v := range d.Data {
		if strings.Contains(k, "serp-init") {
			json.Unmarshal(v, &blob)
		}
	}
	if !lzHasPrefix(blob) {
		t.Fatalf("blob %.20q", blob)
	}
	out, err := LZDecompressFromBase64(blob)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		PageProps struct {
			TotalCount int `json:"totalCount"`
		} `json:"pageProps"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.PageProps.TotalCount != 323 {
		t.Fatalf("decoded JSON: total=%d err=%v", p.PageProps.TotalCount, err)
	}
}
