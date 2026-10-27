package version

import "testing"

func TestString(t *testing.T) {
	old := version
	defer func() { version = old }()

	// A test binary has no module version, so this is "dev" (or a stamped
	// version when run from a module build); either way never empty or "v…".
	version = ""
	if got := String(); got == "" || got[0] == 'v' {
		t.Errorf("String() = %q", got)
	}
	// The -ldflags value wins, and a leading "v" is dropped.
	for in, want := range map[string]string{"1.0.0": "1.0.0", "v1.2.3": "1.2.3"} {
		version = in
		if got := String(); got != want {
			t.Errorf("version %q: String() = %q, want %q", in, got, want)
		}
	}
}
