package warren

// Canonical repo URL normalization (§15.5). Implemented marmot-side ONLY;
// the canonical form is specified alongside the JSON contracts in
// testdata/contracts/README.md so consumers (stave) compare canonical
// strings without reimplementing the rules.

import "strings"

// CanonicalRepoURL reduces a git remote URL to its canonical `host/path`
// form so equivalent spellings compare equal:
//
//	git@github.com:X/Y.git   ≡ https://github.com/x/y ≡
//	ssh://git@github.com/x/y/ → github.com/x/y
//
// Rules: drop the scheme (`https://`, `ssh://`, `git://`, `file://`, …) and
// any user[:password]@ prefix, rewrite the scp-style `host:path` separator
// to `/`, lowercase the host, and strip a trailing `.git` suffix and
// trailing slashes. Local filesystem paths pass through with only the
// trailing-`.git`/slash strip (they have no host). Empty input canonicalizes
// to "".
func CanonicalRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	hadScheme := false
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		hadScheme = true
	}
	// Strip user info: only an '@' before the first '/' belongs to the
	// authority (git@host:path, user:pass@host/path — not path@segment).
	slash := strings.Index(s, "/")
	if at := strings.Index(s, "@"); at >= 0 && (slash < 0 || at < slash) {
		s = s[at+1:]
	}
	// scp-style host:path (no scheme): the first ':' before any '/' is the
	// separator — rewrite it to '/'. Scheme forms keep ':' (a port).
	if !hadScheme {
		slash := strings.Index(s, "/")
		if colon := strings.Index(s, ":"); colon >= 0 && (slash < 0 || colon < slash) {
			s = s[:colon] + "/" + s[colon+1:]
		}
	}
	// Lowercase the host (first segment); the path keeps its case.
	if slash := strings.Index(s, "/"); slash > 0 {
		s = strings.ToLower(s[:slash]) + s[slash:]
	} else if slash < 0 {
		s = strings.ToLower(s)
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimRight(s, "/")
}
