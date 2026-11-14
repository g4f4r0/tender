package main

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/g4f4r0/tender/internal/install"
)

// runUpdate runs "tender update". It needs root.
func runUpdate(args []string, version string, out io.Writer) error {
	fs := newFlagSet("update", "[--version vX.Y.Z]", "Update Tender to the latest release and restart its services. Run as root.")
	target := fs.String("version", "", "`vX.Y.Z` the release to install (default: the latest)")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "update takes no arguments, got %q", fs.Arg(0))
	}
	if os.Geteuid() != 0 {
		return errors.New("update needs root. Run it again with sudo.")
	}
	in, err := install.New(out)
	if err != nil {
		return err
	}
	in.Version = version
	return in.Update(context.Background(), *target)
}
