package web

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

var (
	reDropBlocks = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b.*?</script>`),
		regexp.MustCompile(`(?is)<style\b.*?</style>`),
		regexp.MustCompile(`(?is)<noscript\b.*?</noscript>`),
		regexp.MustCompile(`(?is)<svg\b.*?</svg>`),
		regexp.MustCompile(`(?is)<nav\b.*?</nav>`),
		regexp.MustCompile(`(?is)<header\b.*?</header>`),
		regexp.MustCompile(`(?is)<footer\b.*?</footer>`),
		regexp.MustCompile(`(?is)<!--.*?-->`),
	}
	// Cell ends become " : " so "Miete</td><td>1550 €" reads "Miete : 1550 €".
	reCellEnd  = regexp.MustCompile(`(?i)</(td|th|dt)>`)
	reBlockEnd = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|tr|h[1-6]|dd|section|article|ul|ol|table)>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces   = regexp.MustCompile(`[ \t\x{00a0}\x{202f}]+`)
	reHref     = regexp.MustCompile(`(?i)href\s*=\s*["']([^"'#]+)`)
	reH1       = regexp.MustCompile(`(?is)<h1\b[^>]*>(.*?)</h1>`)
)

// HTMLToText turns an HTML page into readable plain text, one block per line,
// with navigation/header/footer/script content removed.
func HTMLToText(page string) string {
	s := page
	for _, re := range reDropBlocks {
		s = re.ReplaceAllString(s, " ")
	}
	s = reCellEnd.ReplaceAllString(s, " : ")
	s = reBlockEnd.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(reSpaces.ReplaceAllString(line, " "))
		line = strings.Trim(line, ": ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// Links returns absolute, de-duplicated links whose URL matches pattern.
func Links(page, base string, pattern *regexp.Regexp) []string {
	b, _ := url.Parse(base)
	seen := map[string]bool{}
	var out []string
	for _, m := range reHref.FindAllStringSubmatch(page, -1) {
		ref, err := url.Parse(html.UnescapeString(strings.TrimSpace(m[1])))
		if err != nil {
			continue
		}
		abs := ref.String()
		if b != nil {
			abs = b.ResolveReference(ref).String()
		}
		if !pattern.MatchString(abs) || seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// Meta returns the content of <meta property|name="key" content="...">.
func Meta(page, key string) string {
	re := regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property|name)\s*=\s*["']` + regexp.QuoteMeta(key) + `["'][^>]*>`)
	tag := re.FindString(page)
	if tag == "" {
		return ""
	}
	m := regexp.MustCompile(`(?is)content\s*=\s*["']([^"']*)`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

// Title returns the page's <h1> text, falling back to og:title.
func Title(page string) string {
	if m := reH1.FindStringSubmatch(page); m != nil {
		t := strings.TrimSpace(reSpaces.ReplaceAllString(html.UnescapeString(reTag.ReplaceAllString(m[1], " ")), " "))
		if t != "" {
			return t
		}
	}
	return Meta(page, "og:title")
}

// CutAt truncates text at the first occurrence of any marker (case-insensitive)
// that appears after the first 10% of the text. Used to drop "similar listings"
// and teaser sections that would pollute keyword matching.
func CutAt(text string, markers ...string) string {
	minPos := len(text) / 10
	cut := len(text)
	for _, mk := range markers {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(mk))
		for _, loc := range re.FindAllStringIndex(text, -1) {
			if loc[0] >= minPos {
				if loc[0] < cut {
					cut = loc[0]
				}
				break
			}
		}
	}
	return text[:cut]
}
