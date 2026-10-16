package common

import "strings"

// EscapeSegment escapes a literal '%' first, the way URL percent-encoding
// does, so the only way to produce "%2e" or "%2e%2e" is through the special
// cases below: an ordinary path segment can never collide with them.
func EscapeSegment(seg string) string {
	escaped := strings.ReplaceAll(seg, "%", "%25")
	switch escaped {
	case ".":
		return "%2e"
	case "..":
		return "%2e%2e"
	default:
		return escaped
	}
}

// SanitizePathname escapes every "." or ".." segment of key, anywhere in
// the path, not just its basename: kloset's path.Clean-based tree building
// would otherwise collapse such a segment onto the root or a parent
// directory. unsafe reports whether such a segment was found. Every
// segment goes through EscapeSegment, including safe ones, so a key that
// already contains a literal "%2e" can never collide with an escaped one.
func SanitizePathname(key string) (pathname string, unsafe bool) {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		if seg == "." || seg == ".." {
			unsafe = true
		}
		segments[i] = EscapeSegment(seg)
	}
	return "/" + strings.Join(segments, "/"), unsafe
}

// UnescapeSegment reverses EscapeSegment: the two special-cased forms map
// back to their literal "." or "..", and any percent-encoded '%' ("%25")
// is restored. It is only safe to apply to a pathname that actually went
// through SanitizePathname on import - on data from any other source, a
// segment that merely looks like one of our escaped forms would be
// rewritten incorrectly.
func UnescapeSegment(seg string) string {
	switch seg {
	case "%2e":
		return "."
	case "%2e%2e":
		return ".."
	default:
		return strings.ReplaceAll(seg, "%25", "%")
	}
}

// DesanitizePathname reverses SanitizePathname segment by segment.
func DesanitizePathname(pathname string) string {
	segments := strings.Split(pathname, "/")
	for i, seg := range segments {
		segments[i] = UnescapeSegment(seg)
	}
	return strings.Join(segments, "/")
}
