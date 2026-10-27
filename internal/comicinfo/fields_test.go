package comicinfo

import (
	"reflect"
	"strings"
	"testing"
)

func TestSet(t *testing.T) {
	cases := []struct {
		name, field, value string
		want               ComicInfo
		wantErr            string
	}{
		{"string", "Publisher", "Image", ComicInfo{Publisher: "Image"}, ""},
		{"case-insensitive name", "publisher", " Image ", ComicInfo{Publisher: "Image"}, ""},
		{"int", "Year", "2021", ComicInfo{Year: 2021}, ""},
		{"float", "CommunityRating", "4.5", ComicInfo{CommunityRating: 4.5}, ""},
		{"enum canonicalized", "agerating", "mature 17+", ComicInfo{AgeRating: "Mature 17+"}, ""},
		{"manga enum", "Manga", "yesandrighttoleft", ComicInfo{Manga: "YesAndRightToLeft"}, ""},
		{"list cleaned", "Genre", " Horror ,, Sci-Fi ", ComicInfo{Genre: "Horror, Sci-Fi"}, ""},
		{"empty skipped", "Year", "  ", ComicInfo{}, ""},
		{"unknown field", "Description", "x", ComicInfo{}, "unknown field"},
		{"page count not settable", "PageCount", "3", ComicInfo{}, "unknown field"},
		{"bad int", "Year", "20x1", ComicInfo{}, "whole number"},
		{"negative int", "Count", "-1", ComicInfo{}, "whole number"},
		{"rating out of range", "CommunityRating", "6", ComicInfo{}, "0 to 5"},
		{"bad enum", "AgeRating", "10+", ComicInfo{}, "not valid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ci ComicInfo
			err := ci.Set(c.field, c.value)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ci, c.want) {
				t.Errorf("got %+v, want %+v", ci, c.want)
			}
		})
	}
}

func TestGetRoundTrip(t *testing.T) {
	in := ComicInfo{Title: "One", Year: 2021, CommunityRating: 4.5, AgeRating: "Teen"}
	var out ComicInfo
	for _, f := range Fields() {
		if err := out.Set(f, in.Get(f)); err != nil {
			t.Fatalf("Set(%s): %v", f, err)
		}
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip:\n got %+v\nwant %+v", out, in)
	}
	if got := in.Get("Month"); got != "" {
		t.Errorf("Get(unset int) = %q, want empty", got)
	}
}

func TestFields(t *testing.T) {
	f := Fields()
	if f[0] != "Title" || f[len(f)-1] != "GTIN" {
		t.Errorf("Fields not in schema order: %v", f)
	}
	for _, name := range f {
		if name == "PageCount" || name == "Pages" || name == "XMLName" {
			t.Errorf("Fields includes non-settable %s", name)
		}
	}
}
