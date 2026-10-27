package command

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/filename"
	"github.com/AeBiCe/cbztool/internal/provider"
)

// fakeLister adds SeriesIssueLister to fakeProvider.
type fakeLister struct {
	*fakeProvider
	lists map[string][]provider.IssueStub // series id -> issues
}

func (f fakeLister) ListIssues(_ context.Context, id string) ([]provider.IssueStub, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.lists[id], nil
}

func stubs(nums ...string) []provider.IssueStub {
	out := make([]provider.IssueStub, len(nums))
	for i, n := range nums {
		out[i] = provider.IssueStub{ID: "id" + n, Number: n}
	}
	return out
}

func parsedNums(nums ...string) []filename.ParsedName {
	out := make([]filename.ParsedName, len(nums))
	for i, n := range nums {
		out[i] = filename.ParsedName{Series: "Saga", Number: n}
	}
	return out
}

func TestCompareIssues(t *testing.T) {
	g := compareIssues(
		stubs("1", "2", "2", "3", "10", "4", "1.MU", "5"), // "2" twice: a variant
		parsedNums("001", "3", "1.mu", "0.5", "99"),
	)
	if g.total != 7 || g.have != 3 {
		t.Errorf("have %d of %d, want 3 of 7", g.have, g.total)
	}
	if got := strings.Join(g.missing, ","); got != "2,4,5,10" {
		t.Errorf("missing = %s", got)
	}
	if got := strings.Join(g.extra, ","); got != "0.5,99" {
		t.Errorf("extra = %s", got)
	}
}

func TestFormatNumbers(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"1", "2", "4", "5", "6", "7", "9.5", "1.MU"}, "1, 2, 4–7, 9.5, 1.MU"},
		{[]string{"3"}, "3"},
		{[]string{"7", "8"}, "7, 8"},
		{[]string{"1", "2", "3", "5", "6", "7"}, "1–3, 5–7"},
		{nil, ""},
	}
	for _, c := range cases {
		nums := append([]string(nil), c.in...)
		sortNumbers(nums)
		if got := formatNumbers(nums); got != c.want {
			t.Errorf("formatNumbers(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalGaps(t *testing.T) {
	gaps, highest := localGaps(parsedNums("1", "002", "5", "5", "1.5", "annual", ""))
	if highest != 5 || strings.Join(gaps, ",") != "3,4" {
		t.Errorf("gaps = %v, highest = %d", gaps, highest)
	}
	if gaps, highest := localGaps(parsedNums("x")); highest != 0 || gaps != nil {
		t.Errorf("no numbers: %v %d", gaps, highest)
	}
}

func TestLocalIdentityPrefersComicInfo(t *testing.T) {
	dir := t.TempDir()
	ci, _ := (&comicinfo.ComicInfo{Series: "Aliens / Vampirella", Number: "2", Year: 2015}).Marshal()
	tagged := filepath.Join(dir, "av_02_scan.cbz")
	if err := archive.BuildArchive(tagged, map[string][]byte{"001.jpg": {1}, archive.ComicInfoName: ci}, false); err != nil {
		t.Fatal(err)
	}
	if id := localIdentity(tagged); id.Series != "Aliens / Vampirella" || id.Number != "2" || id.Year != 2015 {
		t.Errorf("tagged identity = %+v", id)
	}
	untagged := filepath.Join(dir, "Saga 012 (2013).cbz")
	if err := archive.BuildArchive(untagged, map[string][]byte{"001.jpg": {1}}, false); err != nil {
		t.Fatal(err)
	}
	if id := localIdentity(untagged); id.Series != "Saga" || id.Number != "12" || id.Year != 2013 {
		t.Errorf("filename identity = %+v", id)
	}
}

func TestMissingOnline(t *testing.T) {
	files := []string{"Saga 001.cbz", "Saga 003.cbz", "Paper Girls 001.cbz", "Mystery 001.cbz", "????.cbz"}
	ids := []filename.ParsedName{
		{Series: "Saga", Number: "1"}, {Series: "saga", Number: "3", Year: 2012},
		{Series: "Paper Girls", Number: "1"}, {Series: "Mystery", Number: "1"}, {},
	}
	metron := fakeLister{
		fakeProvider: &fakeProvider{name: "metron", series: []provider.SeriesResult{{ID: "5", Name: "Saga", StartYear: 2012}}},
		lists:        map[string][]provider.IssueStub{"5": stubs("1", "2", "3", "4", "5", "6")},
	}
	out := captureStdout(t, func() {
		if err := missingOnline([]provider.Provider{metron}, files, ids, false); err != nil {
			t.Error(err)
		}
	})
	for _, want := range []string{
		"Saga (2012) · metron 5 — have 2 of 6 issues, missing 4: 2, 4–6",
		`[!] "Paper Girls" — skipped: not found on metron`, // fake search only knows Saga
		"[!] ????.cbz — skipped: no series",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	broken := fakeLister{fakeProvider: &fakeProvider{name: "metron", err: errors.New("401 unauthorized")}}
	var err error
	captureStdout(t, func() { err = missingOnline([]provider.Provider{broken}, files[:2], ids[:2], false) })
	if err == nil || !strings.Contains(err.Error(), "1 of 1 series") {
		t.Errorf("a provider error must fail the run: %v", err)
	}
}

func TestMissingOffline(t *testing.T) {
	out := captureStdout(t, func() {
		missingOffline(
			[]string{"a", "b", "c", "d"},
			[]filename.ParsedName{{Series: "Saga", Number: "1"}, {Series: "Saga", Number: "4"}, {Series: "Saga", Number: "5"}, {Series: "Monstress", Number: "1"}},
		)
	})
	for _, want := range []string{`series "Saga": missing 2 in #1–#5: 2, 3`, `series "Monstress": no gaps in #1–#1`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
