package command

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/provider"
)

func TestIDProvider(t *testing.T) {
	cases := []struct {
		raw, want, pinned string
		wantProv, wantID  string
		wantErrSubstring  string
	}{
		{"14195", cvVolumePrefix, "", "metron", "14195", ""},
		{"4050-160423", cvVolumePrefix, "", "comicvine", "160423", ""},
		{"4000-675976", cvIssuePrefix, "", "comicvine", "675976", ""},
		{"160423", cvVolumePrefix, "cv", "comicvine", "160423", ""}, // bare + -p cv keeps working
		{"4050-160423", cvVolumePrefix, "comicvine", "comicvine", "160423", ""},
		{"4000-675976", cvVolumePrefix, "", "", "", "issue id"}, // issue id to --series-id
		{"4050-160423", cvIssuePrefix, "", "", "", "volume id"}, // volume id to --id
		{"4050-160423", cvVolumePrefix, "mt", "", "", "-p selects metron"},
		{"9999-1", cvVolumePrefix, "", "", "", "unknown prefix"},
		{"abc", cvVolumePrefix, "", "", "", "not a valid id"},
		{"4050-abc", cvVolumePrefix, "", "", "", "not a valid id"},
		{"0", cvIssuePrefix, "", "", "", "not a valid id"},
	}
	for _, c := range cases {
		prov, id, err := idProvider(c.raw, c.want, c.pinned, "--x")
		if c.wantErrSubstring != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErrSubstring) {
				t.Errorf("idProvider(%q, %s, %q) err = %v, want containing %q", c.raw, c.want, c.pinned, err, c.wantErrSubstring)
			}
			continue
		}
		if err != nil || prov != c.wantProv || id != c.wantID {
			t.Errorf("idProvider(%q, %s, %q) = (%q, %q, %v), want (%q, %q)", c.raw, c.want, c.pinned, prov, id, err, c.wantProv, c.wantID)
		}
	}
}

// fakeProvider is an in-memory Provider for exercising combine offline.
type fakeProvider struct {
	name   string
	issues map[string]*provider.Issue // issue id -> issue
	series []provider.SeriesResult    // SearchSeries results
	byNum  map[string]string          // "seriesID/number" -> issue id
	xref   map[string]string          // other provider's id -> this provider's issue id
	err    error                      // returned by every lookup when set
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) SearchSeries(context.Context, string) ([]provider.SeriesResult, error) {
	return f.series, f.err
}
func (f *fakeProvider) IssueByNumber(ctx context.Context, seriesID, number string) (*provider.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	id, ok := f.byNum[seriesID+"/"+number]
	if !ok {
		return nil, fmt.Errorf("no issue: %w", provider.ErrNotFound)
	}
	return f.IssueByID(ctx, id)
}
func (f *fakeProvider) IssueByID(_ context.Context, id string) (*provider.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	iss, ok := f.issues[id]
	if !ok {
		return nil, fmt.Errorf("no issue %s: %w", id, provider.ErrNotFound)
	}
	return iss, nil
}

// fakeXRef adds the reverse-lookup capability.
type fakeXRef struct{ *fakeProvider }

func (f fakeXRef) IssueByXRef(ctx context.Context, _, otherID string) (*provider.Issue, error) {
	id, ok := f.xref[otherID]
	if !ok {
		return nil, fmt.Errorf("no xref: %w", provider.ErrNotFound)
	}
	return f.IssueByID(ctx, id)
}

func TestCombine(t *testing.T) {
	metronIss := &provider.Issue{
		ComicInfo: &comicinfo.ComicInfo{Series: "Saga", Number: "1", Title: "Metron title", Publisher: "Image", Characters: "Alana"},
		ID:        "10", XRef: map[string]string{"comicvine": "20"},
	}
	cvIss := &provider.Issue{
		ComicInfo: &comicinfo.ComicInfo{Series: "Saga", Number: "001", Title: "CV title", Summary: "From CV.", Characters: "Alana, Marko", Locations: "Landfall"},
		ID:        "20",
	}
	metron := fakeXRef{&fakeProvider{name: "metron", issues: map[string]*provider.Issue{"10": metronIss}, xref: map[string]string{"20": "10"}}}
	cv := &fakeProvider{name: "comicvine", issues: map[string]*provider.Issue{"20": cvIss}}
	chain := []provider.Provider{metron, cv}
	ctx := context.Background()

	check := func(t *testing.T, ci *comicinfo.ComicInfo, names []string) {
		t.Helper()
		if strings.Join(names, "+") != "metron+comicvine" {
			t.Errorf("names = %v", names)
		}
		if ci.Title != "Metron title" || ci.Summary != "From CV." || ci.Publisher != "Image" ||
			ci.Characters != "Alana, Marko" || ci.Locations != "Landfall" {
			t.Errorf("merged = %+v", ci)
		}
		if ci.Notes != "Tagged by cbztool: metron issue 10, comicvine issue 20" {
			t.Errorf("Notes = %q", ci.Notes)
		}
	}

	t.Run("metron resolved, cv by cross-reference", func(t *testing.T) {
		ci, names, _ := combine(ctx, sources{chain: chain}, metron, metronIss, false)
		check(t, ci, names)
	})
	t.Run("cv resolved, metron by reverse lookup; metron still wins", func(t *testing.T) {
		ci, names, _ := combine(ctx, sources{chain: chain}, cv, cvIss, false)
		check(t, ci, names)
	})
	t.Run("number mismatch is ignored", func(t *testing.T) {
		bad := &provider.Issue{ComicInfo: &comicinfo.ComicInfo{Number: "2", Summary: "wrong"}, ID: "20"}
		cvBad := &fakeProvider{name: "comicvine", issues: map[string]*provider.Issue{"20": bad}}
		ci, names, notes := combine(ctx, sources{chain: []provider.Provider{metron, cvBad}}, metron, metronIss, false)
		if len(names) != 1 || ci.Summary != "" {
			t.Errorf("mismatched issue merged: names=%v ci=%+v", names, ci)
		}
		if len(notes) != 1 || !notes[0].warn {
			t.Errorf("mismatch not reported as a warning: %+v", notes)
		}
	})
	t.Run("other provider failing keeps the resolved data", func(t *testing.T) {
		broken := &fakeProvider{name: "comicvine", err: fmt.Errorf("503")}
		ci, names, notes := combine(ctx, sources{chain: []provider.Provider{metron, broken}}, metron, metronIss, false)
		if len(names) != 1 || ci.Title != "Metron title" || ci.Notes != "Tagged by cbztool: metron issue 10" {
			t.Errorf("names=%v ci=%+v", names, ci)
		}
		if len(notes) != 1 || !notes[0].warn {
			t.Errorf("failed lookup not reported as a warning: %+v", notes)
		}
	})
	t.Run("no cross-reference: unique exact series name", func(t *testing.T) {
		noXRef := &provider.Issue{ComicInfo: metronIss.ComicInfo, ID: "10"}
		cvByName := &fakeProvider{
			name:   "comicvine",
			issues: map[string]*provider.Issue{"20": cvIss},
			series: []provider.SeriesResult{{ID: "5", Name: "Saga"}, {ID: "6", Name: "Saga Deluxe"}},
			byNum:  map[string]string{"5/1": "20"},
		}
		_, names, _ := combine(ctx, sources{chain: []provider.Provider{metron, cvByName}}, metron, noXRef, false)
		if len(names) != 2 {
			t.Errorf("name match not used: %v", names)
		}
		// Two exact-name candidates: ambiguous, so no guess.
		cvByName.series = append(cvByName.series, provider.SeriesResult{ID: "7", Name: "Saga"})
		_, names, _ = combine(ctx, sources{chain: []provider.Provider{metron, cvByName}}, metron, noXRef, false)
		if len(names) != 1 {
			t.Errorf("ambiguous name match was used: %v", names)
		}
	})
}

func TestApplyPrefer(t *testing.T) {
	merged := &comicinfo.ComicInfo{Title: "Metron title", Summary: "Metron summary.", Year: 2018, Month: 7, Day: 4}
	found := map[string]*provider.Issue{
		"metron":    {ComicInfo: &comicinfo.ComicInfo{Title: "Metron title", Summary: "Metron summary.", Year: 2018, Month: 7, Day: 4}},
		"comicvine": {ComicInfo: &comicinfo.ComicInfo{Title: "", Summary: "CV summary.", Year: 2018, Month: 6}},
	}
	applyPrefer(merged, found, map[string]string{
		"Summary": "comicvine", // overridden
		"Title":   "comicvine", // CV has no title: merged value kept
		"Year":    "comicvine", // whole date from CV, Day included
		"Writer":  "gcd",       // provider didn't contribute: ignored
	})
	if merged.Summary != "CV summary." || merged.Title != "Metron title" ||
		merged.Year != 2018 || merged.Month != 6 || merged.Day != 0 {
		t.Errorf("after prefer = %+v", merged)
	}
}
