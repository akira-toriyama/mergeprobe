package probe

import "testing"

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"git@github.com:akira-toriyama/mergeprobe.git", "akira-toriyama", "mergeprobe", true},
		{"git@github.com:akira-toriyama/mergeprobe", "akira-toriyama", "mergeprobe", true},
		{"https://github.com/akira-toriyama/mergeprobe.git", "akira-toriyama", "mergeprobe", true},
		{"https://github.com/akira-toriyama/mergeprobe", "akira-toriyama", "mergeprobe", true},
		{"ssh://git@github.com/akira-toriyama/mergeprobe.git", "akira-toriyama", "mergeprobe", true},
		{"https://github.com/cli/cli", "cli", "cli", true},
		// case-insensitive host; owner/repo preserved verbatim.
		{"git@GitHub.com:Akira-Toriyama/MergeProbe.git", "Akira-Toriyama", "MergeProbe", true},
		// non-GitHub or malformed.
		{"git@gitlab.com:owner/repo.git", "", "", false},
		{"https://example.com/owner/repo.git", "", "", false},
		{"", "", "", false},
		{"https://github.com/owner", "", "", false},
		{"not a url", "", "", false},
	}
	for _, tc := range tests {
		owner, repo, ok := parseGitHubRepo(tc.url)
		if ok != tc.ok || owner != tc.owner || repo != tc.repo {
			t.Errorf("parseGitHubRepo(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.url, owner, repo, ok, tc.owner, tc.repo, tc.ok)
		}
	}
}

func TestSameRepo(t *testing.T) {
	// owner/repo matching is case-insensitive (GitHub treats them so), with the
	// optional .git suffix ignored on both sides.
	if !sameRepo("akira-toriyama", "mergeprobe", "Akira-Toriyama", "MergeProbe") {
		t.Error("sameRepo should be case-insensitive")
	}
	if sameRepo("cli", "cli", "cli", "go-gh") {
		t.Error("different repos should not match")
	}
}
