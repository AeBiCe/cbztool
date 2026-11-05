package command

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/config"
	"github.com/AeBiCe/cbztool/internal/provider"
)

// TestTagRename: with -r, a tagged file is renamed from the metadata just
// resolved; dry-run only reports the name, and a taken name is a warning
// that leaves the (tagged) file where it is.
func TestTagRename(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{name: "metron", issues: map[string]*provider.Issue{
		"666": {ComicInfo: &comicinfo.ComicInfo{Series: "Batman", Number: "666", Title: "Batman in Bethlehem", Year: 2007}, ID: "666"},
	}}
	resolve := func(ctx context.Context, _ string) (*provider.Issue, error) { return prov.IssueByID(ctx, "666") }
	tmpl, err := parseNameTemplate(config.DefaultTemplate)
	if err != nil {
		t.Fatal(err)
	}
	src := sources{chain: []provider.Provider{prov}, rename: &tagRenamer{tmpl: tmpl, planned: map[string]bool{}}}
	want := filepath.Join(dir, "Batman #666 - Batman in Bethlehem.cbz")

	path := filepath.Join(dir, "Batman 666.cbz")
	makeCBZ(t, path, nil)
	o := tagFileWithResolver(src, prov, path, resolve, true, false, false)
	if len(o.notes) != 1 || o.notes[0].text != "would rename to "+filepath.Base(want) {
		t.Errorf("dry-run notes = %+v", o.notes)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("dry run renamed the file")
	}

	o = tagFileWithResolver(src, prov, path, resolve, false, false, false)
	if o.err != nil || len(o.notes) != 1 || o.notes[0].text != "renamed to "+filepath.Base(want) {
		t.Fatalf("outcome = %+v", o)
	}
	if ci := readCI(t, want); ci.Title != "Batman in Bethlehem" {
		t.Errorf("renamed file's ComicInfo = %+v", ci)
	}

	dup := filepath.Join(dir, "Batman 666 (2007).cbz")
	makeCBZ(t, dup, nil)
	o = tagFileWithResolver(src, prov, dup, resolve, false, false, false)
	if o.err != nil || !o.written || len(o.notes) != 1 || !o.notes[0].warn || !strings.Contains(o.notes[0].text, "already exists") {
		t.Errorf("collision outcome = %+v", o)
	}
	if ci := readCI(t, dup); ci.Series != "Batman" {
		t.Error("a blocked rename should still leave the file tagged")
	}
}

// TestNewTagRenamer: -r on the command line overrides rename.on_tag either way.
func TestNewTagRenamer(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	setOnTag := func(on bool) {
		body := `{"rename": {"on_tag": false}}`
		if on {
			body = `{"rename": {"on_tag": true}}`
		}
		if err := os.MkdirAll(filepath.Join(xdg, "cbztool"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(xdg, "cbztool", "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		onTag bool
		args  []string
		want  bool
	}{
		{false, nil, false},
		{true, nil, true},
		{false, []string{"-r"}, true},
		{true, []string{"-r=false"}, false},
		{true, []string{"--rename=false"}, false},
	}
	for _, c := range cases {
		setOnTag(c.onTag)
		fs := flag.NewFlagSet("tag", flag.ContinueOnError)
		r := fs.Bool("rename", false, "")
		fs.BoolVar(r, "r", false, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		got, err := newTagRenamer(fs, *r)
		if err != nil || (got != nil) != c.want {
			t.Errorf("on_tag=%v %v: renamer = %v, %v; want on=%v", c.onTag, c.args, got, err, c.want)
		}
	}
}
