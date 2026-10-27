// Package filename parses messy real-world comic-book filenames into the
// structured fields a metadata provider needs to find the right issue.
package filename

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ParsedName is the structured result of parsing a comic-book filename.
type ParsedName struct {
	Series string // series/title text, cruft removed (casing preserved)
	Number string // issue number; integers zero-stripped, decimals preserved
	Year   int    // cover/publication year if present in (), else 0
	Volume int    // volume number if a Vol/v marker is present, else 0
}

var (
	// groupRe matches a parenthetical (...) or bracketed [...] cruft group.
	groupRe = regexp.MustCompile(`[(\[][^)\]]*[)\]]`)
	// yearRe matches a 4-digit 19xx/20xx year.
	yearRe = regexp.MustCompile(`(?:19|20)\d{2}`)
	// volRe matches a volume marker: "Vol", "Vol.", "Volume", or bare "v".
	volRe = regexp.MustCompile(`(?i)\b(?:vol(?:ume)?\.?\s*(\d+)|v(\d+))\b`)
	// hashNumRe matches a #-prefixed issue number (preferred when present).
	hashNumRe = regexp.MustCompile(`#(\d+(?:\.\d+)?)\b`)
	// numRe matches any standalone integer/decimal numeric token.
	numRe = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
)

// Parse extracts structured fields from a .cbz filename. It is built to tolerate
// messy real-world names, e.g.:
//
//	Batman #5 (2016) (Digital) (Empire).cbz -> {Batman, 5, 2016, 0}
//	Saga Vol.2 014.cbz                      -> {Saga, 14, 0, 2}
//	Series Name - 012 - The Will (2020).cbz -> {Series Name, 12, 2020, 0}
//	[Group] Title 003 (digital-Empire).cbz  -> {Title, 3, 0, 0}
//	Angor v01 - Runaway (2017).cbz          -> {Angor, 1, 2017, 1}
//
// Algorithm: pull the Year from inside (...) groups, strip all cruft groups,
// extract and remove any volume marker, then take the issue number (a
// #-prefixed token if present, else the last numeric token, else the volume).
// The series is whatever text precedes the chosen number token, cleaned up.
func Parse(name string) ParsedName {
	stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	var p ParsedName

	// Year only counts when it appears inside a () / [] group, so a 4-digit
	// issue number like "2099" isn't mistaken for a year.
	for _, g := range groupRe.FindAllString(stem, -1) {
		if y := yearRe.FindString(g); y != "" {
			p.Year, _ = strconv.Atoi(y)
			break
		}
	}

	work := groupRe.ReplaceAllString(stem, " ")

	volStart := -1
	if m := volRe.FindStringSubmatchIndex(work); m != nil {
		var vs string
		if m[2] >= 0 {
			vs = work[m[2]:m[3]]
		} else {
			vs = work[m[4]:m[5]]
		}
		p.Volume, _ = strconv.Atoi(vs)
		volStart = m[0]
		// Blank with equal-length spaces so volStart stays a valid index.
		work = volRe.ReplaceAllStringFunc(work, func(s string) string {
			return strings.Repeat(" ", len(s))
		})
	}

	numStart := -1
	var raw string
	if loc := hashNumRe.FindStringSubmatchIndex(work); loc != nil {
		raw = work[loc[2]:loc[3]]
		numStart = loc[0] // include the '#' so it's dropped from the series
	} else if locs := numRe.FindAllStringIndex(work, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		raw = work[last[0]:last[1]]
		numStart = last[0]
	} else if volStart >= 0 {
		raw = strconv.Itoa(p.Volume)
		numStart = volStart
	}

	if raw != "" {
		p.Number = normalizeNumber(raw)
		work = work[:numStart]
	}
	p.Series = cleanSeries(work)
	return p
}

// normalizeNumber strips leading zeros from an integer number ("012" -> "12")
// but preserves decimals ("1.5") verbatim.
func normalizeNumber(s string) string {
	if strings.Contains(s, ".") {
		return s
	}
	if n, err := strconv.Atoi(s); err == nil {
		return strconv.Itoa(n)
	}
	return s
}

// cleanSeries normalizes separators and trims leading/trailing punctuation from
// the series text, preserving its original casing.
func cleanSeries(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " -–—.:#")
}
