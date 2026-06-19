package command

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCollectImagesSkipsSymlinks verifies the pack input scanner never follows a
// symlink, so a link disguised as a page image can't exfiltrate a target file's
// contents into the archive (security review finding #2).
func TestCollectImagesSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "001.jpg"), []byte("real page"), 0o644); err != nil {
		t.Fatal(err)
	}

	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "002.jpg")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	entries, images, err := collectImages(dir)
	if err != nil {
		t.Fatalf("collectImages: %v", err)
	}

	if _, ok := entries["002.jpg"]; ok {
		t.Error("symlink 002.jpg was packed; it must be skipped")
	}
	if got := string(entries["001.jpg"]); got != "real page" {
		t.Errorf("001.jpg = %q, want %q", got, "real page")
	}
	if len(images) != 1 || images[0] != "001.jpg" {
		t.Errorf("images = %v, want [001.jpg]", images)
	}
	for name, data := range entries {
		if bytes.Contains(data, []byte("TOPSECRET")) {
			t.Errorf("secret leaked into archive via entry %q", name)
		}
	}
}
