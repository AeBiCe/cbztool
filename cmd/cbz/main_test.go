package main

import (
	"slices"
	"testing"
)

func TestConfigFlag(t *testing.T) {
	cases := []struct {
		args    []string
		rest    []string
		path    string
		wantErr bool
	}{
		{[]string{"tag", "x.cbz"}, []string{"tag", "x.cbz"}, "", false},
		{[]string{"-c", "a.json", "tag"}, []string{"tag"}, "a.json", false},
		{[]string{"--config", "a.json", "tag"}, []string{"tag"}, "a.json", false},
		{[]string{"--config=a.json", "tag"}, []string{"tag"}, "a.json", false},
		{[]string{"-c=a.json"}, []string{}, "a.json", false},
		{[]string{"-c"}, nil, "", true},
		{[]string{"tag", "-c", "a.json"}, []string{"tag", "-c", "a.json"}, "", false}, // only before the command
		{nil, nil, "", false},
	}
	for _, c := range cases {
		rest, path, err := configFlag(c.args)
		if (err != nil) != c.wantErr || path != c.path || !slices.Equal(rest, c.rest) {
			t.Errorf("configFlag(%q) = (%q, %q, %v)", c.args, rest, path, err)
		}
	}
}
