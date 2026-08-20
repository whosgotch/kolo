// Package config says where kolo keeps what a machine remembers: the org it
// serves, the agents running on it, the certificates it has been issued.
//
// One directory under the home directory, rather than the place each operating
// system keeps configuration. ~/.kolo is the same sentence on a Mac and on a
// Linux box, it is short enough to type while reading it off a screen, and
// "delete ~/.kolo and start again" is an instruction somebody can be given over
// chat. os.UserConfigDir is none of those on a Mac.
package config

import (
	"os"
	"path/filepath"
)

// Dir is ~/.kolo, or $KOLO_HOME for a machine that runs more than one org.
func Dir() string {
	if dir := os.Getenv("KOLO_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// No home to put it under is not a reason to refuse to start; the
		// working directory is somewhere, and the path is printed either way.
		return ".kolo"
	}
	return filepath.Join(home, ".kolo")
}

// Path names a file in it.
func Path(name string) string {
	return filepath.Join(Dir(), name)
}
