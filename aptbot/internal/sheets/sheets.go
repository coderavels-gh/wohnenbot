// Package sheets is a minimal Google Sheets v4 client authenticated with a
// service-account key (stdlib only: RS256 JWT → OAuth token).
package sheets

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var api = "https://sheets.googleapis.com/v4/spreadsheets/" // var for tests

type Client struct {
	SpreadsheetID string
	email         string
	tokenURI      string
	key           *rsa.PrivateKey
	http          *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func New(credentialsFile, spreadsheetID string) (*Client, error) {
	raw, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, err
	}
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("service account json: %w", err)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("service account: no PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("service account key is not RSA")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &Client{
		SpreadsheetID: spreadsheetID, email: sa.ClientEmail, tokenURI: sa.TokenURI,
		key: rk, http: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ServiceAccountEmail is the address to share the spreadsheet with.
func (c *Client) ServiceAccountEmail() string { return c.email }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expiry) > time.Minute {
		return c.token, nil
	}
	now := time.Now()
	header := b64([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": c.email, "scope": "https://www.googleapis.com/auth/spreadsheets",
		"aud": c.tokenURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := header + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + b64(sig)}}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.tokenURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("google token: HTTP %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	c.token, c.expiry = tok.AccessToken, now.Add(time.Duration(tok.ExpiresIn)*time.Second)
	return c.token, nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, api+c.SpreadsheetID+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sheets %s %s: HTTP %d: %s", method, path, resp.StatusCode, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func a1(tab, rng string) string {
	return url.PathEscape("'" + strings.ReplaceAll(tab, "'", "''") + "'!" + rng)
}

// EnsureTab creates the tab with a frozen, bold header row if missing.
// statusCol >= 0 adds a dropdown to that column with statusOptions.
func (c *Client) EnsureTab(ctx context.Context, tab string, header []string, statusCol int, statusOptions []string) error {
	var meta struct {
		Sheets []struct {
			Properties struct {
				SheetID int    `json:"sheetId"`
				Title   string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := c.call(ctx, "GET", "?fields=sheets.properties(sheetId,title)", nil, &meta); err != nil {
		return err
	}
	for _, s := range meta.Sheets {
		if s.Properties.Title == tab {
			return nil
		}
	}
	var add struct {
		Replies []struct {
			AddSheet struct {
				Properties struct {
					SheetID int `json:"sheetId"`
				} `json:"properties"`
			} `json:"addSheet"`
		} `json:"replies"`
	}
	err := c.call(ctx, "POST", ":batchUpdate", map[string]any{"requests": []any{
		map[string]any{"addSheet": map[string]any{"properties": map[string]any{"title": tab, "gridProperties": map[string]any{"frozenRowCount": 1}}}},
	}}, &add)
	if err != nil {
		return err
	}
	sheetID := add.Replies[0].AddSheet.Properties.SheetID
	row := make([]any, len(header))
	for i, h := range header {
		row[i] = h
	}
	if err := c.call(ctx, "PUT", "/values/"+a1(tab, "A1")+"?valueInputOption=RAW", map[string]any{"values": [][]any{row}}, nil); err != nil {
		return err
	}
	reqs := []any{
		map[string]any{"repeatCell": map[string]any{
			"range":  map[string]any{"sheetId": sheetID, "startRowIndex": 0, "endRowIndex": 1},
			"cell":   map[string]any{"userEnteredFormat": map[string]any{"textFormat": map[string]any{"bold": true}}},
			"fields": "userEnteredFormat.textFormat.bold",
		}},
	}
	if statusCol >= 0 && len(statusOptions) > 0 {
		vals := make([]any, len(statusOptions))
		for i, o := range statusOptions {
			vals[i] = map[string]any{"userEnteredValue": o}
		}
		reqs = append(reqs, map[string]any{"setDataValidation": map[string]any{
			"range": map[string]any{"sheetId": sheetID, "startRowIndex": 1, "startColumnIndex": statusCol, "endColumnIndex": statusCol + 1},
			"rule":  map[string]any{"condition": map[string]any{"type": "ONE_OF_LIST", "values": vals}, "showCustomUi": true, "strict": false},
		}})
	}
	return c.call(ctx, "POST", ":batchUpdate", map[string]any{"requests": reqs}, nil)
}

// Append writes rows below the last filled row of the tab (USER_ENTERED, so
// dates and numbers are parsed). OVERWRITE fills the existing empty rows, which
// keeps their formatting, banding and conditional rules; nil cells are left
// untouched (formula columns keep working).
func (c *Client) Append(ctx context.Context, tab string, rows [][]any) error {
	_, err := c.AppendAt(ctx, tab, rows)
	return err
}

var reRangeRow = regexp.MustCompile(`![A-Z]+(\d+)`)

// AppendAt is Append that also returns the 1-based row number of the first
// row written (0 if nothing was written).
func (c *Client) AppendAt(ctx context.Context, tab string, rows [][]any) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	var resp struct {
		Updates struct {
			UpdatedRange string `json:"updatedRange"`
		} `json:"updates"`
	}
	err := c.call(ctx, "POST", "/values/"+a1(tab, "A1")+":append?valueInputOption=USER_ENTERED&insertDataOption=OVERWRITE",
		map[string]any{"values": rows}, &resp)
	if err != nil {
		return 0, err
	}
	m := reRangeRow.FindStringSubmatch(resp.Updates.UpdatedRange)
	if m == nil {
		return 0, fmt.Errorf("append: unexpected updatedRange %q", resp.Updates.UpdatedRange)
	}
	row, _ := strconv.Atoi(m[1])
	return row, nil
}

func (c *Client) sheetID(ctx context.Context, tab string) (int, error) {
	var meta struct {
		Sheets []struct {
			Properties struct {
				SheetID int    `json:"sheetId"`
				Title   string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := c.call(ctx, "GET", "?fields=sheets.properties(sheetId,title)", nil, &meta); err != nil {
		return 0, err
	}
	for _, s := range meta.Sheets {
		if s.Properties.Title == tab {
			return s.Properties.SheetID, nil
		}
	}
	return 0, fmt.Errorf("no tab %q", tab)
}

// Link is a cell whose whole text links to URL.
type Link struct {
	Text, URL string
}

// SetLinks writes links into one column (0-based col), starting at the 1-based
// row firstRow, one per row. It sets only the value and the link, so the
// cell's formatting (bold, wrap, colours) is kept. A Link with no URL is
// skipped.
func (c *Client) SetLinks(ctx context.Context, tab string, col, firstRow int, links []Link) error {
	id, err := c.sheetID(ctx, tab)
	if err != nil {
		return err
	}
	var reqs []any
	for i, l := range links {
		if l.URL == "" || l.Text == "" {
			continue
		}
		reqs = append(reqs, map[string]any{"updateCells": map[string]any{
			"start": map[string]any{"sheetId": id, "rowIndex": firstRow - 1 + i, "columnIndex": col},
			"rows": []any{map[string]any{"values": []any{map[string]any{
				"userEnteredValue": map[string]any{"stringValue": l.Text},
				"textFormatRuns":   []any{map[string]any{"startIndex": 0, "format": map[string]any{"link": map[string]any{"uri": l.URL}}}},
			}}}},
			"fields": "userEnteredValue,textFormatRuns",
		}})
	}
	if len(reqs) == 0 {
		return nil
	}
	return c.call(ctx, "POST", ":batchUpdate", map[string]any{"requests": reqs}, nil)
}

// DeleteColumn removes one column (0-based) from a tab.
func (c *Client) DeleteColumn(ctx context.Context, tab string, col int) error {
	id, err := c.sheetID(ctx, tab)
	if err != nil {
		return err
	}
	return c.call(ctx, "POST", ":batchUpdate", map[string]any{"requests": []any{map[string]any{"deleteDimension": map[string]any{
		"range": map[string]any{"sheetId": id, "dimension": "COLUMNS", "startIndex": col, "endIndex": col + 1},
	}}}}, nil)
}

// Column returns all values of one column (e.g. "A"), header included.
func (c *Client) Column(ctx context.Context, tab, col string) ([]string, error) {
	var resp struct {
		Values [][]any `json:"values"`
	}
	if err := c.call(ctx, "GET", "/values/"+a1(tab, col+":"+col), nil, &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Values))
	for _, r := range resp.Values {
		if len(r) > 0 {
			out = append(out, fmt.Sprint(r[0]))
		}
	}
	return out, nil
}

// Rows returns the cells of a range (e.g. "A:G") as strings, header included.
func (c *Client) Rows(ctx context.Context, tab, rng string) ([][]string, error) {
	var resp struct {
		Values [][]any `json:"values"`
	}
	if err := c.call(ctx, "GET", "/values/"+a1(tab, rng), nil, &resp); err != nil {
		return nil, err
	}
	out := make([][]string, len(resp.Values))
	for i, r := range resp.Values {
		out[i] = make([]string, len(r))
		for j, v := range r {
			out[i][j] = fmt.Sprint(v)
		}
	}
	return out, nil
}

// Header returns the first row of a tab as displayed (formula headers give
// their result).
func (c *Client) Header(ctx context.Context, tab string) ([]string, error) {
	rows, err := c.Rows(ctx, tab, "1:1")
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}
