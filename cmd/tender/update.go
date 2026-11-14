package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/g4f4r0/tender/internal/install"
)

// runUpdate runs "tender update". It needs root.
func runUpdate(args []string, version string, out io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	target := fs.String("version", "", "the release to install, such as v0.1.0 (default: the latest)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: tender update [--version vX.Y.Z]")
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root: run it with sudo, for example: sudo tender update")
	}
	in, err := install.New(out)
	if err != nil {
		return err
	}
	in.Version = version
	return in.Update(context.Background(), *target)
}
