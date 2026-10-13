package k8s

import (
	"path"
	"strings"
)

// rebaseExcludes translates the ignore patterns handed to the k8s importer,
// which are written against the pathnames as they appear in the snapshot
// (rooted at prefix), into patterns for the importer running in the pod,
// which walks the volume mounted at podpath.
func rebaseExcludes(excludes []string, prefix, podpath string) []string {
	var rebased []string
	for _, pattern := range excludes {
		if p, ok := rebaseExclude(pattern, prefix, podpath); ok {
			rebased = append(rebased, p)
		}
	}
	return rebased
}

// rebaseExclude rebases one gitignore pattern.  It returns false when the
// pattern is a no-op, or when it can't match anything under prefix.
func rebaseExclude(pattern, prefix, podpath string) (string, bool) {
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		// blank line or comment: not a rule.
		return "", false
	}

	// mirror gitignore.ParsePattern: pull apart the negation prefix
	// and the trailing slash so that we're left with the path part.
	body, negate := strings.CutPrefix(pattern, "!")
	if !strings.HasSuffix(body, "\\ ") {
		body = strings.TrimRight(body, " ")
	}
	body, dironly := strings.CutSuffix(body, "/")

	// non-anchored patterns, and those starting with "**/", match
	// at any depth, leave them as-is.
	if !strings.Contains(body, "/") || strings.HasPrefix(body, "**/") {
		return pattern, true
	}

	// anything else is anchored at the snapshot root: consume the
	// prefix, component by component, and re-root what's left.
	comps := strings.Split(strings.TrimPrefix(body, "/"), "/")
	for _, comp := range components(prefix) {
		if len(comps) == 0 {
			// the pattern names a parent of the mount point:
			// the whole volume is excluded.
			break
		}
		if comps[0] == "**" {
			// "**" swallows the rest of the prefix and keeps
			// matching at any depth below it.
			break
		}
		if ok, err := path.Match(comps[0], comp); err != nil || !ok {
			return "", false
		}
		comps = comps[1:]
	}

	body = podpath
	if len(comps) != 0 {
		body += "/" + strings.Join(comps, "/")
	}
	if dironly {
		body += "/"
	}
	if negate {
		body = "!" + body
	}
	return body, true
}

// components splits a path into its non-empty components.
func components(p string) []string {
	var comps []string
	for _, comp := range strings.Split(p, "/") {
		if comp != "" && comp != "." {
			comps = append(comps, comp)
		}
	}
	return comps
}
