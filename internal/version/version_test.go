package version

import "testing"

// A from-source build (no -ldflags injection) reports Version "dev"; Resolve may
// backfill Commit/Date from the test binary's VCS stamp but must never panic and
// must leave the tag untouched.
func TestResolveDefaultsToDev(t *testing.T) {
	if got := Resolve().Version; got != "dev" {
		t.Errorf("Version = %q, want %q", got, "dev")
	}
}

// The release contract: when the linker injects Version/Commit/Date, Resolve
// returns them verbatim and does NOT let the runtime VCS stamp clobber the
// shipped identity. Pins the early-return that guards this.
func TestResolveInjectedIdentityWins(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })
	Version, Commit, Date = "v9.9.9", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2030-01-02T03:04:05Z"

	got := Resolve()
	want := Info{Version: "v9.9.9", Commit: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Date: "2030-01-02T03:04:05Z"}
	if got != want {
		t.Errorf("Resolve() = %+v, want the injected identity %+v (VCS stamp must not clobber it)", got, want)
	}
}

// String must render each identity shape: bare tag, +short commit (7-char
// truncation), +date, and +modified, plus the early return when no commit is set.
func TestInfoString(t *testing.T) {
	cases := []struct {
		name string
		in   Info
		want string
	}{
		{"tag only", Info{Version: "dev"}, "dev"},
		{"short commit", Info{Version: "dev", Commit: "abc1234"}, "dev (abc1234)"},
		{"long commit truncated to 7", Info{Version: "v1.2.3", Commit: "0123456789abcdef"}, "v1.2.3 (0123456)"},
		{"commit and date", Info{Version: "dev", Commit: "abc1234", Date: "2026-07-10T00:00:00Z"}, "dev (abc1234, 2026-07-10T00:00:00Z)"},
		{"modified", Info{Version: "dev", Commit: "abc1234", Date: "2026-07-10T00:00:00Z", Modified: true}, "dev (abc1234, 2026-07-10T00:00:00Z, modified)"},
		{"modified without date", Info{Version: "dev", Commit: "0123456789ab", Modified: true}, "dev (0123456, modified)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}
