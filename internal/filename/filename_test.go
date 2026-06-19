package filename

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want ParsedName
	}{
		// Acceptance examples.
		{"Batman #5 (2016) (Digital) (Empire).cbz", ParsedName{Series: "Batman", Number: "5", Year: 2016}},
		{"Saga Vol.2 014.cbz", ParsedName{Series: "Saga", Number: "14", Volume: 2}},
		{"Series Name - 012 - The Will (2020).cbz", ParsedName{Series: "Series Name", Number: "12", Year: 2020}},
		{"[Group] Title 003 (digital-Empire).cbz", ParsedName{Series: "Title", Number: "3"}},
		{"squirrel-man 3.cbz", ParsedName{Series: "squirrel-man", Number: "3"}},
		{"Rogue 001 (2026).cbz", ParsedName{Series: "Rogue", Number: "1", Year: 2026}},

		// Edge cases.
		{"Cover.cbz", ParsedName{Series: "Cover"}},                                       // no number
		{"Series 1.5.cbz", ParsedName{Series: "Series", Number: "1.5"}},                  // decimal preserved
		{"Spider-Man 2099 #12.cbz", ParsedName{Series: "Spider-Man 2099", Number: "12"}}, // digits in series
		{"Series Annual 1.cbz", ParsedName{Series: "Series Annual", Number: "1"}},        // special
		{"X-Men 2 (2019) (Digital).cbz", ParsedName{Series: "X-Men", Number: "2", Year: 2019}},
		{"under_score_series 007.cbz", ParsedName{Series: "under score series", Number: "7"}},
		{"  Spaced Series  4  .cbz", ParsedName{Series: "Spaced Series", Number: "4"}},
		{"Series Vol 3 #10 (2021).cbz", ParsedName{Series: "Series", Number: "10", Year: 2021, Volume: 3}},
		{"Series (2020).cbz", ParsedName{Series: "Series", Year: 2020}}, // year only, no number
	}

	for _, c := range cases {
		got := Parse(c.in)
		if got != c.want {
			t.Errorf("Parse(%q) =\n  got  %+v\n  want %+v", c.in, got, c.want)
		}
	}
}
