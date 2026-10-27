package command

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/comicinfo"
)

// setFields returns a fake editor that sets the given fields in the JSON.
func setFields(t *testing.T, set map[string]string) func(string) error {
	return func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("editor got invalid JSON: %v\n%s", err, data)
		}
		for k, v := range set {
			m[k] = v
		}
		out, _ := json.Marshal(m)
		return os.WriteFile(path, out, 0o600)
	}
}

func answers(s string) *prompter { return &prompter{r: bufio.NewReader(strings.NewReader(s))} }

func editFixture(t *testing.T, name string, ci *comicinfo.ComicInfo) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	entries := map[string][]byte{"001.png": pngPage(t, 4, 6)}
	if ci != nil {
		data, err := ci.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		entries[archive.ComicInfoName] = data
	}
	if err := archive.BuildArchive(p, entries, false); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEditChangesAndClears(t *testing.T) {
	path := editFixture(t, "saga.cbz", &comicinfo.ComicInfo{Series: "Saga", Number: "1", Title: "Old", Summary: "Gone soon", Genre: "Sci-Fi,Fantasy"})
	if err := editOne(path, setFields(t, map[string]string{"Title": "New", "Summary": "", "Year": "2012"}), answers("")); err != nil {
		t.Fatal(err)
	}
	ci := readCI(t, path)
	if ci.Title != "New" || ci.Summary != "" || ci.Year != 2012 || ci.Series != "Saga" || ci.Genre != "Sci-Fi, Fantasy" {
		t.Errorf("after edit: %+v", ci)
	}
	if ci.PageCount != 1 || ci.Pages == nil {
		t.Errorf("PageCount/Pages not computed: %d %+v", ci.PageCount, ci.Pages)
	}
}

func TestEditNoChanges(t *testing.T) {
	// "Sci-Fi,Fantasy" is tidied to "Sci-Fi, Fantasy" on read-back; that alone
	// must not count as an edit.
	path := editFixture(t, "saga.cbz", &comicinfo.ComicInfo{Series: "Saga", Number: "1", Genre: "Sci-Fi,Fantasy"})
	before, _ := os.ReadFile(path)
	if err := editOne(path, setFields(t, nil), answers("")); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("an unchanged edit rewrote the archive")
	}
}

func TestEditUntaggedPrefillsFromFilename(t *testing.T) {
	path := editFixture(t, "Saga 012 (2013).cbz", nil)
	var seen map[string]string
	peek := func(p string) error {
		data, _ := os.ReadFile(p)
		return json.Unmarshal(data, &seen)
	}
	if err := editOne(path, peek, answers("")); err != nil {
		t.Fatal(err)
	}
	if seen["Series"] != "Saga" || seen["Number"] != "12" {
		t.Errorf("prefill = %q #%q", seen["Series"], seen["Number"])
	}
	if _, ok := seen["PageCount"]; ok {
		t.Error("PageCount is computed and must not be offered for editing")
	}
}

func TestEditInvalidRetryAndAbort(t *testing.T) {
	path := editFixture(t, "saga.cbz", &comicinfo.ComicInfo{Series: "Saga", Number: "1"})
	calls := 0
	breakThenFix := func(p string) error {
		calls++
		if calls == 1 {
			return os.WriteFile(p, []byte(`{"Series": "Saga", "AgeRating": "Nope"}`), 0o600)
		}
		return os.WriteFile(p, []byte(`{"Series": "Saga", "Number": "1", "AgeRating": "teen"}`), 0o600)
	}
	if err := editOne(path, breakThenFix, answers("\n")); err != nil {
		t.Fatal(err)
	}
	if ci := readCI(t, path); calls != 2 || ci.AgeRating != "Teen" {
		t.Errorf("calls=%d AgeRating=%q", calls, ci.AgeRating)
	}

	before, _ := os.ReadFile(path)
	broken := func(p string) error { return os.WriteFile(p, []byte(`{"Series": `), 0o600) }
	err := editOne(path, broken, answers("n\n"))
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want aborted", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("an aborted edit changed the archive")
	}
	// The user's edits are kept in the temp file named in the error.
	kept := err.Error()[strings.LastIndex(err.Error(), " ")+1 : len(err.Error())-1]
	if data, rerr := os.ReadFile(kept); rerr != nil || string(data) != `{"Series": ` {
		t.Errorf("edits not kept at %q: %v", kept, rerr)
	}
	os.Remove(kept)
}

func TestParseEditDoc(t *testing.T) {
	for _, bad := range []string{`[]`, `null`, `{"Title": "x"} {}`, `{"Bogus": "x"}`, `{"Year": true}`} {
		if _, err := parseEditDoc([]byte(bad)); err == nil {
			t.Errorf("parseEditDoc(%s) accepted", bad)
		}
	}
	ci, err := parseEditDoc([]byte(`{"Series": "Saga", "Year": 2012, "Count": "", "Summary": null}`))
	if err != nil || ci.Series != "Saga" || ci.Year != 2012 {
		t.Errorf("got %+v, %v", ci, err)
	}
}
