package main

import (
	"fmt"
	"io"
	"strings"
)

// A link rather than a path: most people running kolo have no checkout.
const referenceURL = "https://github.com/whosgotch/kolo/blob/main/docs/reference.md"

// Prose broken at a width a terminal is not asked to reflow.
func wrap(w io.Writer, indent, text string) {
	const width = 76
	line := indent
	for _, word := range strings.Fields(text) {
		if len(line)+len(word) > width && len(line) > len(indent) {
			fmt.Fprintln(w, line)
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	if len(line) > len(indent) {
		fmt.Fprintln(w, line)
	}
}

func english(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func verb(names []string, one, many string) string {
	if len(names) == 1 {
		return one
	}
	return many
}

func unique(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
