package provider

import (
	"encoding/json"
	"testing"
)

// sampleMetronIssue mirrors the subset of fields cb consumes from a Metron
// /issue/{id}/ response, so the JSON tags and mapping are verified offline.
const sampleMetronIssue = `{
  "id": 123,
  "number": "5",
  "name": ["The Will", "Prologue"],
  "cover_date": "2016-03-01",
  "store_date": "2016-03-02",
  "page": 32,
  "desc": "An issue.",
  "upc": "76194130001",
  "cv_id": 9999,
  "resource_url": "https://metron.cloud/issue/saga-2014-5/",
  "publisher": {"name": "Image Comics"},
  "series": {"name": "Saga", "volume": 1, "year_began": 2014,
             "genres": [{"name": "Fantasy"}, {"name": "Drama"}],
             "series_type": {"name": "Ongoing Series"}},
  "rating": {"name": "Mature"},
  "credits": [
    {"creator": "Brian K. Vaughan", "role": [{"name": "Writer"}]},
    {"creator": "Fiona Staples", "role": [{"name": "Penciller"}, {"name": "Cover"}]},
    {"creator": "Fonografiks", "role": [{"name": "Letterer"}]}
  ],
  "characters": [{"name": "Alana"}, {"name": "Marko"}],
  "teams": [{"name": "Wreath"}],
  "arcs": [{"name": "Volume 1"}]
}`

func TestMetronIssueMapping(t *testing.T) {
	var iss metronIssue
	if err := json.Unmarshal([]byte(sampleMetronIssue), &iss); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ci := metronIssueToComicInfo(&iss)

	checks := map[string]struct{ got, want string }{
		"Series":      {ci.Series, "Saga"},
		"Number":      {ci.Number, "5"},
		"Title":       {ci.Title, "The Will; Prologue"},
		"Summary":     {ci.Summary, "An issue."},
		"Publisher":   {ci.Publisher, "Image Comics"},
		"Genre":       {ci.Genre, "Fantasy, Drama"},
		"Format":      {ci.Format, "Ongoing Series"},
		"Web":         {ci.Web, "https://metron.cloud/issue/saga-2014-5/"},
		"Characters":  {ci.Characters, "Alana, Marko"},
		"Teams":       {ci.Teams, "Wreath"},
		"StoryArc":    {ci.StoryArc, "Volume 1"},
		"AgeRating":   {ci.AgeRating, "Mature 17+"},
		"GTIN":        {ci.GTIN, "76194130001"},
		"Writer":      {ci.Writer, "Brian K. Vaughan"},
		"Penciller":   {ci.Penciller, "Fiona Staples"},
		"CoverArtist": {ci.CoverArtist, "Fiona Staples"},
		"Letterer":    {ci.Letterer, "Fonografiks"},
	}
	for field, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", field, c.got, c.want)
		}
	}
	if ci.PageCount != 32 {
		t.Errorf("PageCount = %d, want 32", ci.PageCount)
	}
	if ci.Volume != 1 {
		t.Errorf("Volume = %d, want 1", ci.Volume)
	}
	if ci.Year != 2016 || ci.Month != 3 || ci.Day != 1 {
		t.Errorf("date = %d-%d-%d, want 2016-3-1", ci.Year, ci.Month, ci.Day)
	}
}

func TestMapMetronRating(t *testing.T) {
	cases := map[string]string{
		"Everyone": "Everyone",
		"Teen":     "Teen",
		"Mature":   "Mature 17+",
		"Explicit": "Adults Only 18+",
		"Unknown":  "",
		"":         "",
	}
	for in, want := range cases {
		if got := mapMetronRating(in); got != want {
			t.Errorf("mapMetronRating(%q) = %q, want %q", in, got, want)
		}
	}
}
