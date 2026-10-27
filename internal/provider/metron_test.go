package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sampleMetronIssue mirrors the subset of fields cbz consumes from a Metron
// /issue/{id}/ response, so the JSON tags and mapping are verified offline.
const sampleMetronIssue = `{
  "id": 123,
  "number": "5",
  "alt_number": "55",
  "name": ["The Will", "Prologue"],
  "cover_date": "2016-03-01",
  "store_date": "2016-03-02",
  "page": 32,
  "desc": "An issue.",
  "upc": "76194130001",
  "cv_id": 9999,
  "resource_url": "https://metron.cloud/issue/saga-2014-5/",
  "publisher": {"name": "Image Comics"},
  "imprint": {"name": "Skybound"},
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
		"Imprint":     {ci.Imprint, "Skybound"},
		"AltNumber":   {ci.AlternateNumber, "55"},
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
	if ci.Year != 2016 || ci.Month != 3 || ci.Day != 2 {
		t.Errorf("date = %d-%d-%d, want 2016-3-2 (store date)", ci.Year, ci.Month, ci.Day)
	}
}

func TestMetronIssueXRef(t *testing.T) {
	var iss metronIssue
	if err := json.Unmarshal([]byte(sampleMetronIssue), &iss); err != nil {
		t.Fatal(err)
	}
	out := metronIssueToIssue(&iss)
	if out.ID != "123" || out.XRef["comicvine"] != "9999" {
		t.Errorf("ID/XRef = %q/%v, want 123/comicvine:9999", out.ID, out.XRef)
	}
	// A null imprint and no cv_id map to empty values, not errors.
	var bare metronIssue
	if err := json.Unmarshal([]byte(`{"id": 1, "imprint": null}`), &bare); err != nil {
		t.Fatal(err)
	}
	if b := metronIssueToIssue(&bare); b.XRef != nil || b.Imprint != "" {
		t.Errorf("bare issue = %+v / %v", b.ComicInfo, b.XRef)
	}
}

func TestMetronIssueByXRef(t *testing.T) {
	cases := []struct {
		name, list, detail string
		wantErr            bool
	}{
		{"match", `{"results":[{"id":123}]}`, sampleMetronIssue, false},
		{"none", `{"results":[]}`, "", true},
		{"ambiguous", `{"results":[{"id":1},{"id":2}]}`, "", true},
		// The server ignored the filter: the detail's cv_id doesn't match.
		{"filter ignored", `{"results":[{"id":123}]}`, strings.Replace(sampleMetronIssue, `"cv_id": 9999`, `"cv_id": 1`, 1), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/issue/" {
					if r.URL.Query().Get("cv_id") != "9999" {
						t.Errorf("cv_id filter = %q", r.URL.Query().Get("cv_id"))
					}
					w.Write([]byte(c.list))
					return
				}
				w.Write([]byte(c.detail))
			}))
			defer srv.Close()
			iss, err := newTestMetron(srv).IssueByXRef(context.Background(), "comicvine", "9999")
			if c.wantErr {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil || iss.ID != "123" {
				t.Fatalf("IssueByXRef = %+v, %v", iss, err)
			}
		})
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
