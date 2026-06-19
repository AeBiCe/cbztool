package command

import "testing"

func TestSanitize(t *testing.T) {
	const r = "�" // replacement char that sanitize substitutes

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "Saga 012.jpg", "Saga 012.jpg"},
		{"legit unicode passes through", "café 日本語 — naïve.png", "café 日本語 — naïve.png"},
		{"ansi escape neutralized", "evil\x1b[31mRED\x1b[0m", "evil" + r + "[31mRED" + r + "[0m"},
		{"tab and newline neutralized", "a\tb\nc", "a" + r + "b" + r + "c"},
		{"carriage return neutralized", "overwrite\rme", "overwrite" + r + "me"},
		{"del and c1 neutralized", "x\x7f\x9by", "x" + r + r + "y"},
		{"rtl override neutralized", "photo_‮gpj.exe", "photo_" + r + "gpj.exe"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := sanitize(c.in); got != c.want {
			t.Errorf("%s: sanitize(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
