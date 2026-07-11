package probe

import (
	"net/url"
	"strings"
)

// parseGitHubRepo extracts owner/repo from a github.com remote URL in any of
// git's spellings (scp-like git@host:owner/repo, https://…, ssh://…), returning
// ok=false for a non-GitHub or malformed URL. Owner and repo are returned
// verbatim — GitHub treats them case-insensitively, which is sameRepo's job, not
// this parser's.
func parseGitHubRepo(remote string) (owner, repo string, ok bool) {
	s := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	var host, path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", "", false
		}
		host, path = u.Host, u.Path
	case strings.Contains(s, ":"):
		// scp-like: [user@]host:owner/repo (no scheme, single colon after host).
		i := strings.IndexByte(s, ':')
		host, path = s[:i], s[i+1:]
	default:
		return "", "", false
	}
	// Drop userinfo and any port so only the bare host remains.
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if colon := strings.IndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	if !strings.EqualFold(host, "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// sameRepo reports whether two owner/repo pairs name the same GitHub repository,
// compared case-insensitively (GitHub is case-insensitive on both halves).
func sameRepo(o1, r1, o2, r2 string) bool {
	return strings.EqualFold(o1, o2) && strings.EqualFold(r1, r2)
}
