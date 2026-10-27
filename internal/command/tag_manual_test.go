package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/filename"
)

func TestStripManualFlag(t *testing.T) {
	cases := []struct {
		in       []string
		wantRest string
		wantOK   bool
	}{
		{[]string{"-m", "x.cbz"}, "x.cbz", true},
		{[]string{"--force", "--manual", "x.cbz"}, "--force x.cbz", true},
		{[]string{"-p", "cv", "x.cbz"}, "-p cv x.cbz", false},
		{[]string{"--", "-m"}, "-- -m", false},
	}
	for _, c := range cases {
		rest, ok := stripManualFlag(c.in)
		if strings.Join(rest, " ") != c.wantRest || ok != c.wantOK {
			t.Errorf("stripManualFlag(%v) = (%v, %v), want (%q, %v)", c.in, rest, ok, c.wantRest, c.wantOK)
		}
	}
}

func TestParseMetaFile(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string
	}{
		{"series only", `{"series":{"Publisher":"Image"}}`, ""},
		{"issues only", `{"issues":{"1":{"Title":"One"}}}`, ""},
		{"case-insensitive fields", `{"series":{"publisher":"Image","agerating":"teen"}}`, ""},
		{"empty values skipped", `{"series":{"Year":"","AgeRating":""},"issues":{"1":{"Month":""}}}`, ""},
		{"number values", `{"series":{"Year":2021,"CommunityRating":4.5}}`, ""},
		{"null value", `{"series":{"Year":null}}`, ""},
		{"unknown field", `{"series":{"Description":"x"}}`, "unknown field"},
		{"unknown top-level", `{"serie":{}}`, "unknown field"},
		{"bad age rating", `{"series":{"AgeRating":"10+"}}`, "not valid"},
		{"bad number", `{"series":{"Year":"soon"}}`, "whole number"},
		{"bool value", `{"series":{"Manga":true}}`, "string or number"},
		{"same field twice", `{"series":{"Title":"a","title":"b"}}`, "same field"},
		{"duplicate issue keys", `{"issues":{"1":{},"001":{}}}`, "same issue"},
		{"empty document", `{}`, "no \"series\" or \"issues\""},
		{"trailing data", `{"series":{}} {}`, "unexpected data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseMetaFile("meta.json", []byte(c.json))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestMetaIssueFor(t *testing.T) {
	m, err := parseMetaFile("meta.json", []byte(`{"issues": {"002": {"Title": "Two"}, "Saga 010.cbz": {"Title": "Ten"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ci, _ := m.issueFor("/x/Saga 002.cbz", filename.ParsedName{Number: "2"}); ci == nil || ci.Title != "Two" {
		t.Errorf("number lookup = %+v, want Title Two", ci)
	}
	// An exact filename key wins over the parsed number.
	if ci, _ := m.issueFor("/x/Saga 010.cbz", filename.ParsedName{Number: "2"}); ci == nil || ci.Title != "Ten" {
		t.Errorf("filename lookup = %+v, want Title Ten", ci)
	}
	if ci, _ := m.issueFor("/x/Saga 003.cbz", filename.ParsedName{Number: "3"}); ci != nil {
		t.Errorf("unmatched lookup = %+v, want nil", ci)
	}
}

func TestBuildManual(t *testing.T) {
	existing := &comicinfo.ComicInfo{Series: "Saga", Number: "1", Writer: "Brian K. Vaughan", Summary: "old", PageCount: 5}
	series := &comicinfo.ComicInfo{Publisher: "Image", Year: 2012, Summary: "series"}
	issue := &comicinfo.ComicInfo{Summary: "issue"}
	sets := &comicinfo.ComicInfo{AgeRating: "Teen"}
	parsed := filename.ParsedName{Series: "Saga", Number: "1"}

	got := buildManual(existing, parsed, 42, series, issue, sets)
	want := comicinfo.ComicInfo{Series: "Saga", Number: "1", Writer: "Brian K. Vaughan", Publisher: "Image", Year: 2012, Summary: "issue", AgeRating: "Teen", PageCount: 42}
	if *got != want {
		t.Errorf("layered:\n got %+v\nwant %+v", *got, want)
	}

	// No existing ComicInfo: Series/Number fall back to the filename.
	got = buildManual(nil, filename.ParsedName{Series: "Saga", Number: "3"}, 7, series, nil, nil)
	if got.Series != "Saga" || got.Number != "3" || got.Summary != "series" || got.PageCount != 7 {
		t.Errorf("fresh build = %+v", got)
	}
}

func TestParseSets(t *testing.T) {
	ci, err := parseSets([]string{"AgeRating=teen", "Title=A=B", "Title=Final"})
	if err != nil {
		t.Fatal(err)
	}
	if ci.AgeRating != "Teen" || ci.Title != "Final" {
		t.Errorf("got %+v", ci)
	}
	if _, err := parseSets([]string{"NoEquals"}); err == nil {
		t.Error("expected error for a pair without '='")
	}
	if _, err := parseSets([]string{"Bogus=1"}); err == nil {
		t.Error("expected error for an unknown field")
	}
}

// makeCBZ builds a one-page archive, optionally with a ComicInfo.xml.
func makeCBZ(t *testing.T, path string, ci *comicinfo.ComicInfo) {
	t.Helper()
	entries := map[string][]byte{"001.jpg": []byte("page")}
	if ci != nil {
		data, err := ci.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		entries[archive.ComicInfoName] = data
	}
	if err := archive.BuildArchive(path, entries, false); err != nil {
		t.Fatal(err)
	}
}

func readCI(t *testing.T, path string) *comicinfo.ComicInfo {
	t.Helper()
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ci, _, found, err := a.ReadComicInfo()
	if err != nil || !found {
		t.Fatalf("%s: found=%v err=%v", path, found, err)
	}
	return ci
}

func TestTagApply(t *testing.T) {
	dir := t.TempDir()
	makeCBZ(t, filepath.Join(dir, "Saga 001.cbz"), nil)
	makeCBZ(t, filepath.Join(dir, "Saga 002.cbz"), &comicinfo.ComicInfo{Series: "Saga", Number: "2", Writer: "BKV"})
	meta := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(meta, []byte(`{"series":{"Publisher":"Image","Year":""},"issues":{"2":{"Title":"Two"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tagApply(meta, []string{"AgeRating=Teen"}, dir, false); err != nil {
		t.Fatal(err)
	}

	one := readCI(t, filepath.Join(dir, "Saga 001.cbz"))
	if one.Publisher != "Image" || one.Series != "Saga" || one.Number != "1" || one.AgeRating != "Teen" || one.Title != "" || one.PageCount != 1 {
		t.Errorf("Saga 001: got %+v", one)
	}
	two := readCI(t, filepath.Join(dir, "Saga 002.cbz"))
	if two.Title != "Two" || two.Writer != "BKV" || two.Publisher != "Image" || two.AgeRating != "Teen" {
		t.Errorf("Saga 002 (existing fields must survive): got %+v", two)
	}
}

func TestTemplateBlank(t *testing.T) {
	out := filepath.Join(t.TempDir(), "meta.json")
	if err := writeTemplate(out, "", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Applying an untouched blank template must be valid and change nothing.
	m, err := parseMetaFile(out, data)
	if err != nil {
		t.Fatalf("blank template doesn't parse: %v\n%s", err, data)
	}
	if *m.Series != (comicinfo.ComicInfo{}) || len(m.Issues) != 1 || *m.Issues["1"] != (comicinfo.ComicInfo{}) {
		t.Errorf("blank template isn't empty: series=%+v issues=%v", m.Series, m.Issues)
	}
	for _, want := range []string{`"Publisher": ""`, `"AgeRating": ""`, `"Summary": ""`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("template missing %s", want)
		}
	}
	if strings.Contains(string(data), `"PageCount"`) || strings.Contains(string(data), `"Number"`) {
		t.Errorf("template lists PageCount or Number:\n%s", data)
	}

	// Never overwrite without --force; refuse a .cbz output path.
	if err := writeTemplate(out, "", false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrite without force: err = %v", err)
	}
	if err := writeTemplate(out, "", true); err != nil {
		t.Errorf("overwrite with force: %v", err)
	}
	if err := writeTemplate(filepath.Join(t.TempDir(), "comic.cbz"), "", true); err == nil {
		t.Error("expected refusal of a .cbz template path")
	}
}

func TestTemplatePrefilled(t *testing.T) {
	dir := t.TempDir()
	makeCBZ(t, filepath.Join(dir, "Saga 001.cbz"), &comicinfo.ComicInfo{Series: "Saga", Publisher: "Image", Writer: "BKV", Title: "One"})
	makeCBZ(t, filepath.Join(dir, "Saga 002.cbz"), &comicinfo.ComicInfo{Series: "Saga", Publisher: "Image", Writer: "Someone Else"})
	makeCBZ(t, filepath.Join(dir, "Saga 002 (variant).cbz"), nil)

	out := filepath.Join(t.TempDir(), "meta.json")
	if err := writeTemplate(out, dir, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseMetaFile(out, data)
	if err != nil {
		t.Fatalf("prefilled template doesn't parse: %v\n%s", err, data)
	}
	// Series is shared (the untagged variant defaults it from its filename);
	// Publisher/Writer differ (the variant has none), so they move per-issue.
	if m.Series.Series != "Saga" || m.Series.Publisher != "" || m.Series.Writer != "" {
		t.Errorf("series block = %+v", m.Series)
	}
	one := m.Issues["1"]
	if one == nil || one.Title != "One" || one.Writer != "BKV" || one.Publisher != "Image" {
		t.Errorf(`issues["1"] = %+v`, one)
	}
	// Two files share number 2, so both are keyed by filename.
	if m.Issues["Saga 002.cbz"] == nil || m.Issues["Saga 002 (variant).cbz"] == nil {
		t.Errorf("shared numbers not keyed by filename: %v\n%s", m.Issues, data)
	}
	if m.Issues["Saga 002.cbz"].Writer != "Someone Else" {
		t.Errorf(`issues["Saga 002.cbz"] = %+v`, m.Issues["Saga 002.cbz"])
	}

	// Applying the template back reproduces the existing metadata.
	if err := tagApply(out, nil, dir, false); err != nil {
		t.Fatal(err)
	}
	if ci := readCI(t, filepath.Join(dir, "Saga 002.cbz")); ci.Writer != "Someone Else" || ci.Publisher != "Image" {
		t.Errorf("round trip changed Saga 002: %+v", ci)
	}
}
