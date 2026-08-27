package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
)

func versionCmd(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo version")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nSays which build this is. It is the first thing a bug report needs,")
		fmt.Fprintln(os.Stderr, "and kolo doctor prints the same line at the top of its report.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Println(versionLine())
	return nil
}

// versionLine is what this binary calls itself, with the platform and the Go
// it was built by. A report naming the version alone usually needs a second
// message asking for the other two.
func versionLine() string {
	return fmt.Sprintf("kolo %s %s/%s %s", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
