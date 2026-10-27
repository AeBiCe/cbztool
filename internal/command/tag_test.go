package command

import (
	"testing"

	"codeberg.org/Nikode/cbztool/internal/provider"
)

func TestEditionMarker(t *testing.T) {
	cases := map[string]string{
		"Rogue Sun":                 "",
		"Rogue Sun TPB":             "TPB",
		"Rogues HC":                 "HC",
		"Kid Flash: Going Rogue GN": "GN",
		"Saga Compendium":           "COMPENDIUM",
		"Ultimate Spider-Man":       "", // "Man" must not match anything
	}
	for in, want := range cases {
		if got := editionMarker(in); got != want {
			t.Errorf("editionMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreferEdition(t *testing.T) {
	results := []provider.SeriesResult{
		{Name: "Rogue Sun (2022)", StartYear: 2022},
		{Name: "Rogue Sun TPB (2022)", StartYear: 2022},
		{Name: "Rogues HC (2022)", StartYear: 2022},
	}

	// Filename without an edition marker drops TPB/HC variants.
	got := preferEdition(results, "Rogue Sun")
	if len(got) != 1 || got[0].Name != "Rogue Sun (2022)" {
		t.Errorf("no-marker filter = %+v, want only the singles series", got)
	}

	// Filename with TPB keeps only the TPB variant.
	got = preferEdition(results, "Rogue Sun TPB")
	if len(got) != 1 || got[0].Name != "Rogue Sun TPB (2022)" {
		t.Errorf("TPB filter = %+v, want only the TPB series", got)
	}

	// If filtering would empty the set, the originals are kept.
	onlyTPB := []provider.SeriesResult{{Name: "Some Book TPB (2020)"}}
	if got := preferEdition(onlyTPB, "Some Book"); len(got) != 1 {
		t.Errorf("empty-filter fallback = %+v, want originals preserved", got)
	}
}

func TestBestSeriesMatch(t *testing.T) {
	results := []provider.SeriesResult{
		{ID: "590", Name: "Rogue (1995)", StartYear: 1995},
		{ID: "2393", Name: "Rogue (2001)", StartYear: 2001},
		{ID: "14433", Name: "Rogue (2026)", StartYear: 2026},
	}
	// Three same-named series; the year disambiguates to one.
	if s, ok := bestSeriesMatch(results, "Rogue", 2026); !ok || s.ID != "14433" {
		t.Errorf("bestSeriesMatch year tiebreak = %+v ok=%v, want id 14433", s, ok)
	}
	// Without a year, three exact matches stay ambiguous.
	if _, ok := bestSeriesMatch(results, "Rogue", 0); ok {
		t.Errorf("bestSeriesMatch should be ambiguous without a year")
	}
	// A single exact name auto-matches regardless of year.
	one := []provider.SeriesResult{{ID: "1", Name: "Saga (2012)", StartYear: 2012}}
	if s, ok := bestSeriesMatch(one, "Saga", 0); !ok || s.ID != "1" {
		t.Errorf("bestSeriesMatch single = %+v ok=%v, want id 1", s, ok)
	}

	// Separator punctuation in the provider's name must not defeat a match: a
	// filename's bare "Aliens Vampirella" should resolve "Aliens / Vampirella".
	sep := []provider.SeriesResult{{ID: "14195", Name: "Aliens / Vampirella (2015)", StartYear: 2015}}
	if s, ok := bestSeriesMatch(sep, "Aliens Vampirella", 2015); !ok || s.ID != "14195" {
		t.Errorf("bestSeriesMatch separator-insensitive = %+v ok=%v, want id 14195", s, ok)
	}
}

func TestMatchKey(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Aliens / Vampirella", "Aliens Vampirella"},
		{"Spider-Man", "Spider Man"},
		{"X-Men: Blue", "X Men Blue"},
		{"  Saga  ", "saga"},
	}
	for _, c := range cases {
		if matchKey(c.a) != matchKey(c.b) {
			t.Errorf("matchKey(%q)=%q != matchKey(%q)=%q", c.a, matchKey(c.a), c.b, matchKey(c.b))
		}
	}
	// Distinct names must stay distinct.
	if matchKey("Aliens") == matchKey("Aliens Vampirella") {
		t.Errorf("matchKey collapsed distinct names")
	}
}

func TestCanonNumber(t *testing.T) {
	same := [][2]string{
		{"001", "1"},     // filename zero-pad vs provider bare integer
		{"05", "5"},      // ditto
		{" 5 ", "5"},     // surrounding whitespace
		{"1.MU", "1.mu"}, // alphanumeric suffix, case-folded
	}
	for _, p := range same {
		if canonNumber(p[0]) != canonNumber(p[1]) {
			t.Errorf("canonNumber(%q)=%q != canonNumber(%q)=%q", p[0], canonNumber(p[0]), p[1], canonNumber(p[1]))
		}
	}
	// Decimals are not integers: 1 and 1.5 must stay distinct.
	if canonNumber("1") == canonNumber("1.5") {
		t.Errorf("canonNumber collapsed 1 and 1.5")
	}
}

func TestClassifyIssueCount(t *testing.T) {
	cases := []struct {
		local, reported int
		want            seriesCountVerdict
	}{
		{0, 0, countUnknown},  // provider reported nothing
		{3, 0, countUnknown},  // ditto, even with local files
		{6, 6, countComplete}, // full run
		{3, 800, countIncomplete},
		{7, 6, countOver}, // more local than the series has
	}
	for _, c := range cases {
		if got := classifyIssueCount(c.local, c.reported); got != c.want {
			t.Errorf("classifyIssueCount(%d,%d)=%v, want %v", c.local, c.reported, got, c.want)
		}
	}
}

func TestGroupBySeries(t *testing.T) {
	files := []string{
		"Batman 001 (2011).cbz",
		"Batman 002 (2011).cbz",
		"Batman 002 (2011) (variant).cbz", // same number, must not inflate distinct count
		"Superman 003.cbz",
		"003.cbz", // no series -> ungroupable
	}
	groups, ungroupable := groupBySeries(files)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (Batman, Superman)", len(groups))
	}
	if len(ungroupable) != 1 {
		t.Errorf("got %d ungroupable, want 1", len(ungroupable))
	}
	// Group order is first-seen.
	if groups[0].display != "Batman" || len(groups[0].files) != 3 {
		t.Errorf("group 0 = %q with %d files, want Batman with 3", groups[0].display, len(groups[0].files))
	}
	// The variant shares number 2, so Batman has 2 distinct numbers, not 3.
	if n := distinctNumbers(groups[0].parsed); n != 2 {
		t.Errorf("Batman distinct numbers = %d, want 2", n)
	}
	if groups[0].repYear() != 2011 {
		t.Errorf("Batman repYear = %d, want 2011", groups[0].repYear())
	}
}
