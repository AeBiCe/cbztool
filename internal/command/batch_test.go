package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchDone(t *testing.T) {
	b := newBatch(4, false)
	var err error
	out := captureStdout(t, func() {
		b.record("a.cbz", fileOutcome{written: true})
		b.record("b.cbz", fileOutcome{skip: skipAlreadyTagged})
		b.record("c.cbz", fileOutcome{skip: "series not matched"})
		err = b.done()
	})
	if b.n != 3 {
		t.Errorf("position = %d, want 3", b.n)
	}
	if err != nil {
		t.Errorf("skips must not fail the run: %v", err)
	}
	for _, want := range []string{
		"    [1/4] a.cbz — tagged\n",
		"    [2/4] b.cbz — skipped: " + skipAlreadyTagged + "\n", // routine skip: no marker
		"[!] [3/4] c.cbz — skipped: series not matched\n",
		"done: 1 tagged, 2 skipped, 0 failed\nskipped:\n",
		"  [!] c.cbz — series not matched\n", // skips stay listed in the summary
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	b = newBatch(2, false)
	captureStdout(t, func() {
		b.record("a.cbz", fileOutcome{written: true})
		b.record("b.cbz", fileOutcome{err: errors.New("boom")})
		err = b.done()
	})
	if err == nil || !strings.Contains(err.Error(), "1 of 2 file(s) failed") {
		t.Errorf("done() = %v, want a failure error", err)
	}
}

func TestFailedErr(t *testing.T) {
	if failedErr(0, 3) != nil {
		t.Error("no failures must be nil")
	}
	if err := failedErr(2, 3); err == nil || err.Error() != "2 of 3 file(s) failed" {
		t.Errorf("failedErr(2, 3) = %v", err)
	}
}

// TestTagApplyExitStatus: a directory run with one broken archive still tags
// the rest but returns an error (non-zero exit) so scripts can notice.
func TestTagApplyExitStatus(t *testing.T) {
	dir := t.TempDir()
	makeCBZ(t, filepath.Join(dir, "Saga 001.cbz"), nil)
	if err := os.WriteFile(filepath.Join(dir, "Saga 002.cbz"), []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := tagApply("", []string{"Publisher=Image"}, dir, false)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 file(s) failed") {
		t.Errorf("tagApply = %v, want a failure error", err)
	}
	if ci := readCI(t, filepath.Join(dir, "Saga 001.cbz")); ci.Publisher != "Image" {
		t.Errorf("the good archive wasn't tagged: %+v", ci)
	}
}
