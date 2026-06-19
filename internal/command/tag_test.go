package command

import (
	"testing"

	"cb/internal/provider"
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
}
