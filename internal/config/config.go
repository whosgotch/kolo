// Package config locates kolo's directory.
package config

import (
	"os"
	"path/filepath"
)

// Dir is ~/.kolo, or $KOLO_HOME.
func Dir() string {
	if dir := os.Getenv("KOLO_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kolo"
	}
	return filepath.Join(home, ".kolo")
}

func Path(name string) string {
	return filepath.Join(Dir(), name)
}
