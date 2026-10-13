package listing

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

// DataVersion is stored with each encoded listing. Bump it when a source
// parser changes what it extracts, so stored listings are fetched again
// instead of being re-evaluated from outdated data. Changes to the criteria
// or to the evaluation rules need no bump: those re-run on the stored data.
const DataVersion = 1

// maxData keeps an encoded listing below the 50,000-character limit of a
// Google Sheets cell.
const maxData = 45000

type stored struct {
	V int      `json:"v"`
	L *Listing `json:"l"`
}

// Encode packs the extracted listing (not its checks or verdict) into a
// compact string for the Seen record: JSON, gzip, base64. It returns "" if
// the result would be too big for a sheet cell.
func (l *Listing) Encode() string {
	c := *l
	c.Checks, c.Verdict = nil, ""
	raw, err := json.Marshal(stored{V: DataVersion, L: &c})
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(raw)
	zw.Close()
	s := base64.RawStdEncoding.EncodeToString(buf.Bytes())
	if len(s) > maxData {
		return ""
	}
	return s
}

var ErrStale = errors.New("stored listing data is from an older parser version")

// Decode unpacks a string written by Encode.
func Decode(s string) (*Listing, error) {
	if s == "" {
		return nil, errors.New("no stored listing data")
	}
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 10<<20))
	if err != nil {
		return nil, err
	}
	var st stored
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	if st.V != DataVersion || st.L == nil {
		return nil, ErrStale
	}
	return st.L, nil
}
