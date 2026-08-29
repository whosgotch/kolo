package main

// Turning what kolo found into lines a person reads: how names are said
// together, and where the lines break.

import (
	"fmt"
	"io"
	"strings"
)

// referenceURL is the docs as a link rather than a path. Almost everybody
// running kolo installed a binary and has no checkout to read docs/ out of,
// so a bare docs/reference.md names a file that is not on their machine.
const referenceURL = "https://github.com/whosgotch/kolo/blob/main/docs/reference.md"

// wrap prints prose broken at a width a terminal is not asked to reflow.
// What kolo has to say about an org names its agents and its links, which
// are as long as somebody made them, so where the lines fall can't be
// written by hand.
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

// english joins names the way they would be said aloud.
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
