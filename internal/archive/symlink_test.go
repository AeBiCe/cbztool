package archive

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRewriteArchiveFollowsSymlink: editing through a symlink must change the
// file it points to and leave the link itself in place (a plain rename would
// replace the link with a regular file and leave the real archive untouched).
func TestRewriteArchiveFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "store", "comic.cbz")
	if err := os.Mkdir(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := BuildArchive(real, map[string][]byte{"001.jpg": []byte("page")}, false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "library", "comic.cbz")
	if err := os.Mkdir(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	keepAll := func(string) bool { return true }
	if err := RewriteArchive(link, keepAll, map[string][]byte{ComicInfoName: []byte("<ComicInfo/>")}); err != nil {
		t.Fatal(err)
	}

	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced (mode %v, err %v)", fi.Mode(), err)
	}
	a, err := Open(real)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, _, found, _ := a.ReadComicInfo(); !found {
		t.Error("the symlink's target was not edited")
	}
	// No temp file may be left behind in either directory.
	for _, d := range []string{filepath.Dir(real), filepath.Dir(link)} {
		entries, _ := os.ReadDir(d)
		if len(entries) != 1 {
			t.Errorf("%s holds %d entries, want 1 (stray temp file?)", d, len(entries))
		}
	}
}

// TestBuildArchiveForceThroughSymlink: overwriting an existing output via
// --force follows a symlink the same way.
func TestBuildArchiveForceThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.cbz")
	if err := BuildArchive(real, map[string][]byte{"001.jpg": []byte("old")}, false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.cbz")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := BuildArchive(link, map[string][]byte{"001.jpg": []byte("new"), "002.jpg": []byte("new")}, true); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced (err %v)", err)
	}
	a, err := Open(real)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if n := a.PageCount(); n != 2 {
		t.Errorf("target has %d pages, want 2 (not overwritten through the link)", n)
	}
}

func TestRealTargetRejectsNonRegular(t *testing.T) {
	if _, err := realTarget(t.TempDir()); err == nil {
		t.Error("a directory was accepted as an edit target")
	}
	if _, err := realTarget(filepath.Join(t.TempDir(), "missing.cbz")); err == nil {
		t.Error("a missing file was accepted")
	}
}

// TestBuildArchiveMode: a new archive is 0644 (not CreateTemp's 0600); an
// overwritten one keeps its permissions.
func TestBuildArchiveMode(t *testing.T) {
	out := filepath.Join(t.TempDir(), "new.cbz")
	if err := BuildArchive(out, map[string][]byte{"001.jpg": []byte("p")}, false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o644 {
		t.Errorf("new archive mode = %v, want 0644", fi.Mode().Perm())
	}
	if err := os.Chmod(out, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := BuildArchive(out, map[string][]byte{"001.jpg": []byte("q")}, true); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o640 {
		t.Errorf("overwritten archive mode = %v, want 0640 kept", fi.Mode().Perm())
	}
}
