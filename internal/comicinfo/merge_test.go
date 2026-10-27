package comicinfo

import (
	"reflect"
	"slices"
	"testing"
)

func TestMerge(t *testing.T) {
	metron := &ComicInfo{
		Series: "Death of the Inhumans", Number: "1", Title: "Chapter One: Vox",
		Summary: "Metron summary.", Publisher: "Marvel", AgeRating: "Teen",
		Year: 2018, Month: 7, Day: 4,
		Writer: "Donny Cates", CoverArtist: "Ariel Olivetti, Kaare Andrews",
		Penciller:  "Ariel Olivetti",
		Characters: "Black Bolt, Medusa (Marvel), Karnak", Teams: "Inhumans",
		Web: "https://metron.cloud/issue/x/",
	}
	cv := &ComicInfo{
		Series: "Death of the Inhumans", Number: "1", Title: "Chapter One",
		Summary: "ComicVine summary.", Notes: "cv notes",
		Year: 2018, Month: 9, Day: 1,
		Writer: "Donny Cates", CoverArtist: "Kaare Andrews, Russell Dauterman",
		Penciller: "Russell Dauterman", Inker: "Ariel Olivetti",
		Characters: "Black Bolt, Medusa, Lockjaw", Locations: "Attilan",
		Web: "https://comicvine.gamespot.com/x/",
	}
	got := Merge(metron, cv)
	want := &ComicInfo{
		Series: "Death of the Inhumans", Number: "1", Title: "Chapter One: Vox",
		Summary: "Metron summary.", Notes: "cv notes", Publisher: "Marvel", AgeRating: "Teen",
		Year: 2018, Month: 7, Day: 4,
		// Creator roles: primary's list kept whole (no cover-artist bleed into
		// Penciller); an empty primary role (Inker) is filled from secondary.
		Writer:      "Donny Cates",
		CoverArtist: "Ariel Olivetti, Kaare Andrews",
		Penciller:   "Ariel Olivetti",
		Inker:       "Ariel Olivetti",
		Characters:  "Black Bolt, Medusa (Marvel), Karnak, Lockjaw",
		Teams:       "Inhumans", Locations: "Attilan",
		Web: "https://metron.cloud/issue/x/ https://comicvine.gamespot.com/x/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge:\n got %+v\nwant %+v", got, want)
	}
	if metron.Characters != "Black Bolt, Medusa (Marvel), Karnak" {
		t.Errorf("Merge modified its input: %q", metron.Characters)
	}
}

func TestMergeDateUnit(t *testing.T) {
	// Primary has only a year: the secondary's month/day must not be mixed in.
	got := Merge(&ComicInfo{Year: 2020}, &ComicInfo{Year: 2019, Month: 5, Day: 3})
	if got.Year != 2020 || got.Month != 0 || got.Day != 0 {
		t.Errorf("date = %d-%d-%d, want 2020-0-0", got.Year, got.Month, got.Day)
	}
	// Primary has no date: secondary's is used whole.
	got = Merge(&ComicInfo{}, &ComicInfo{Year: 2019, Month: 5, Day: 3})
	if got.Year != 2019 || got.Month != 5 || got.Day != 3 {
		t.Errorf("date = %d-%d-%d, want 2019-5-3", got.Year, got.Month, got.Day)
	}
}

func TestMergeNil(t *testing.T) {
	a := &ComicInfo{Series: "A"}
	if got := Merge(a, nil); got.Series != "A" || got == a {
		t.Errorf("Merge(a, nil) = %+v (must be a copy)", got)
	}
	if got := Merge(nil, a); got.Series != "A" {
		t.Errorf("Merge(nil, a) = %+v", got)
	}
}

func TestUnionCSV(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", ""},
		{"A, B", "", "A, B"},
		{"", "A, B", "A, B"},
		{"Spider-Man", "Spider Man, Venom", "Spider-Man, Venom"},
		{"batman", "Batman (Bruce Wayne)", "batman"},
		{"A, a", "A", "A"},                      // duplicates within one source too
		{"(Unnamed)", "(Unnamed)", "(Unnamed)"}, // pure parenthetical keeps its key
		{" X ,, Y ", "", "X, Y"},
	}
	for _, c := range cases {
		if got := UnionCSV(c.a, c.b); got != c.want {
			t.Errorf("UnionCSV(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// TestIsUnionField keeps IsUnionField in step with the fields Merge unions.
func TestIsUnionField(t *testing.T) {
	var ci ComicInfo
	ptrs := ci.unionFields()
	v := reflect.ValueOf(&ci).Elem()
	var union []string
	for i := range v.NumField() {
		if f := v.Field(i); f.Kind() == reflect.String && slices.Contains(ptrs, f.Addr().Interface().(*string)) {
			union = append(union, v.Type().Field(i).Name)
		}
	}
	for _, name := range Fields() {
		if IsUnionField(name) != slices.Contains(union, name) {
			t.Errorf("IsUnionField(%q) = %v, but Merge unions: %v", name, IsUnionField(name), union)
		}
	}
}
