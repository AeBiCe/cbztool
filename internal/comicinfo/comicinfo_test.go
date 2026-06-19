package comicinfo

import (
	"strings"
	"testing"
)

func TestComicInfoOmitEmpty(t *testing.T) {
	ci := &ComicInfo{Series: "Squirrel-Man", Number: "3", PageCount: 12}
	out, err := ci.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	if strings.Contains(s, "<Title>") {
		t.Errorf("empty Title should be omitted, got:\n%s", s)
	}
	if !strings.Contains(s, "<Series>Squirrel-Man</Series>") {
		t.Errorf("Series element missing, got:\n%s", s)
	}
	if !strings.Contains(s, "<PageCount>12</PageCount>") {
		t.Errorf("PageCount element missing, got:\n%s", s)
	}
}

func TestComicInfoElementOrder(t *testing.T) {
	ci := &ComicInfo{
		Title: "T", Series: "S", Number: "5", Volume: 1,
		Year: 2016, Writer: "W", Penciller: "P", Publisher: "Pub",
		Genre: "Fantasy", PageCount: 30, AgeRating: "Teen", GTIN: "123",
	}
	out, err := ci.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	order := []string{
		"<Title>", "<Series>", "<Number>", "<Volume>", "<Year>",
		"<Writer>", "<Penciller>", "<Publisher>", "<Genre>",
		"<PageCount>", "<AgeRating>", "<GTIN>",
	}
	last := -1
	for _, tag := range order {
		idx := strings.Index(s, tag)
		if idx < 0 {
			t.Fatalf("missing %s in:\n%s", tag, s)
		}
		if idx < last {
			t.Errorf("%s is out of XSD document order:\n%s", tag, s)
		}
		last = idx
	}
	t.Logf("marshalled ComicInfo:\n%s", s)
}

func TestComicInfoRoundTrip(t *testing.T) {
	in := &ComicInfo{Series: "Squirrel-Man", Number: "3", PageCount: 12, AgeRating: "Everyone"}
	out, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Series != in.Series || got.Number != in.Number ||
		got.PageCount != in.PageCount || got.AgeRating != in.AgeRating {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}
