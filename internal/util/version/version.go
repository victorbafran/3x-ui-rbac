// Package version compares 3x-ui release versions ("v3.8.0" or "3.8.0").
package version

import (
	"strconv"
	"strings"
)

// Compare returns -1, 0 or 1 as a is older than, equal to or newer than b, and
// false when either side is not a plain MAJOR.MINOR.PATCH version.
func Compare(a, b string) (int, bool) {
	aParts, okA := parse(a)
	bParts, okB := parse(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range len(aParts) {
		if aParts[i] > bParts[i] {
			return 1, true
		}
		if aParts[i] < bParts[i] {
			return -1, true
		}
	}
	return 0, true
}

// Normalize strips surrounding space and a leading "v" from a version tag.
func Normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func parse(v string) ([3]int, bool) {
	var result [3]int
	parts := strings.Split(Normalize(v), ".")
	if len(parts) != 3 {
		return result, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, true
}

// CompareRelease is Compare for this fork's release tags, "v3.10.2-rbac42": the
// base version first, then the number after it; a bare version counts as 0.
func CompareRelease(a, b string) (int, bool) {
	aBase, aN, okA := splitRelease(a)
	bBase, bN, okB := splitRelease(b)
	if !okA || !okB {
		return 0, false
	}
	if cmp, ok := Compare(aBase, bBase); !ok || cmp != 0 {
		return cmp, ok
	}
	switch {
	case aN > bN:
		return 1, true
	case aN < bN:
		return -1, true
	}
	return 0, true
}

func splitRelease(v string) (string, int, bool) {
	base, suffix, found := strings.Cut(Normalize(v), "-")
	if !found {
		return base, 0, true
	}
	digits := strings.TrimLeftFunc(suffix, func(r rune) bool { return r < '0' || r > '9' })
	n, err := strconv.Atoi(digits)
	if err != nil || digits == "" || len(digits) == len(suffix) {
		return "", 0, false
	}
	return base, n, true
}
