package relay

import "strings"

// A country reads faster with its flag in front of it, and the flag survives
// the whole way to the customer: measured on ocserv 1.2.4, a group label of
// "🇳🇱 Netherlands" reaches an AnyConnect client byte for byte, and the real
// Cisco Secure Client renders it.

// FlagFor turns a two-letter country code into its flag.
//
// A flag emoji is simply its country's two letters written as regional
// indicator symbols, so there is no table to keep up to date and no country
// that can be missing from one.
func FlagFor(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return ""
	}
	var out strings.Builder
	for _, c := range code {
		if c < 'A' || c > 'Z' {
			return ""
		}
		// U+1F1E6 is the regional indicator for A; the rest follow it.
		out.WriteRune(0x1F1E6 + (c - 'A'))
	}
	return out.String()
}

// LabelFor is what a customer should see for a relay in a country: the flag
// and the country's name. Falls back to whichever half is known, because a
// label that is only a flag tells somebody with no emoji font nothing at all,
// and a lookup that failed should not leave the entry blank.
func LabelFor(countryName, countryCode string) string {
	name := strings.TrimSpace(countryName)
	flag := FlagFor(countryCode)
	switch {
	case flag != "" && name != "":
		return flag + " " + name
	case name != "":
		return name
	case flag != "":
		return flag
	default:
		return ""
	}
}

// SplitLabel takes a label back apart into its flag and its name; one with no
// flag in front is all name.
func SplitLabel(label string) (flag, name string) {
	label = strings.TrimSpace(label)
	first, rest, _ := strings.Cut(label, " ")
	if first == "" {
		return "", label
	}
	for _, r := range first {
		if r < 0x1F1E6 || r > 0x1F1FF {
			return "", label
		}
	}
	return first, strings.TrimSpace(rest)
}
