package term

import (
	"os"
	"testing"
)

func TestMarkersPlainWhenNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := Warn(f); got != "[!]" {
		t.Errorf("Warn = %q, want [!]", got)
	}
	if got := Error(f); got != "[X]" {
		t.Errorf("Error = %q, want [X]", got)
	}
}

func TestNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if Color(os.Stdout) {
		t.Error("NO_COLOR must disable color")
	}
}
