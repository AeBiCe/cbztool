package command

import (
	"strings"
	"testing"
)

func TestPageProblems(t *testing.T) {
	cases := []struct {
		name       string
		images     []string
		wantMax    int
		wantProbs  []string // substrings that must appear, in order-independent fashion
		noProblems bool
	}{
		{
			name:       "contiguous sequence is clean",
			images:     []string{"001.jpg", "002.jpg", "003.jpg"},
			wantMax:    3,
			noProblems: true,
		},
		{
			name:      "real gap is reported",
			images:    []string{"001.jpg", "003.jpg"},
			wantMax:   3,
			wantProbs: []string{"missing: 2"},
		},
		{
			// A cover whose trailing digits are a year must not inflate the
			// missing list with a 4..2015 gap (security review / backlog fix).
			name:      "year-numbered cover treated as unnumbered, not a 2000-page gap",
			images:    []string{"001.jpg", "002.jpg", "003.jpg", "Batman (2016).jpg"},
			wantMax:   3,
			wantProbs: []string{"no page number: Batman (2016).jpg"},
		},
		{
			name:      "does not start at 1",
			images:    []string{"002.jpg", "003.jpg"},
			wantMax:   3,
			wantProbs: []string{"does not start at page 1"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probs, max := pageProblems(c.images)
			if max != c.wantMax {
				t.Errorf("maxNum = %d, want %d", max, c.wantMax)
			}
			joined := strings.Join(probs, "\n")
			if c.noProblems && len(probs) != 0 {
				t.Errorf("expected no problems, got: %q", joined)
			}
			for _, want := range c.wantProbs {
				if !strings.Contains(joined, want) {
					t.Errorf("problems %q missing %q", joined, want)
				}
			}
			// The outlier guard must never produce an absurd missing range.
			if strings.Contains(joined, "2015") {
				t.Errorf("missing list was inflated by an outlier: %q", joined)
			}
		})
	}
}
