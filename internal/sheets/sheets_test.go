package sheets

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSheetsFlow(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	var calls []string
	tabs := map[string]bool{"Sheet1": true}
	var appended []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/token":
			if !strings.Contains(string(body), "assertion=") {
				t.Error("no assertion")
			}
			w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(401)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/sid"):
			var sheets []map[string]any
			for name := range tabs {
				sheets = append(sheets, map[string]any{"properties": map[string]any{"sheetId": 1, "title": name}})
			}
			json.NewEncoder(w).Encode(map[string]any{"sheets": sheets})
		case strings.HasSuffix(r.URL.Path, ":batchUpdate"):
			if strings.Contains(string(body), "addSheet") {
				tabs["Listings"] = true
				w.Write([]byte(`{"replies":[{"addSheet":{"properties":{"sheetId":7}}}]}`))
				return
			}
			if !strings.Contains(string(body), "ONE_OF_LIST") {
				t.Error("no dropdown")
			}
			w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, ":append"):
			var v map[string]any
			json.Unmarshal(body, &v)
			appended = v["values"].([]any)
			w.Write([]byte(`{}`))
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/values/"):
			w.Write([]byte(`{"values":[["Key"],["a:1"],["b:2"]]}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	api = srv.URL + "/v4/spreadsheets/"

	creds, _ := json.Marshal(map[string]string{"client_email": "bot@x.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": srv.URL + "/token"})
	path := filepath.Join(t.TempDir(), "sa.json")
	os.WriteFile(path, creds, 0o600)

	c, err := New(path, "sid")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.EnsureTab(ctx, "Listings", []string{"A", "B"}, 1, []string{"New", "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureTab(ctx, "Listings", []string{"A", "B"}, 1, nil); err != nil { // exists now: no-op
		t.Fatal(err)
	}
	if err := c.Append(ctx, "Listings", [][]any{{"x", 1}}); err != nil || len(appended) != 1 {
		t.Fatalf("append err=%v appended=%v", err, appended)
	}
	col, err := c.Column(ctx, "Seen", "A")
	if err != nil || len(col) != 3 {
		t.Fatalf("column %v %v", col, err)
	}
	tokenCalls := 0
	for _, cl := range calls {
		if strings.HasSuffix(cl, "/token") {
			tokenCalls++
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("token should be cached, got %d token calls", tokenCalls)
	}
}
