// Package cmd is the command tree of synq-cube.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/getsynq/synq-cube/internal/buildinfo"
	"github.com/getsynq/synq-cube/internal/quality"
	"github.com/getsynq/synq-cube/internal/region"
)

// build is the stamped build of this binary. internal/buildinfo is the only home of the stamps,
// so `--version` and the uploader fields of a snapshot report the same values.
var build = buildinfo.Get()

// Deps are the parts of the process that a test replaces.
type Deps struct {
	// Stdin is what `--input -` reads. Nil means the standard input of the process.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// QualityOptions change how the upload connects to Coalesce Quality.
	QualityOptions []quality.Option
}

// NewRootCommand builds the command tree.
func NewRootCommand(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   "synq-cube",
		Short: "Send the data model of a Cube deployment to Coalesce Quality",
		Long: `Send the data model of a Cube deployment to Coalesce Quality.

synq-cube reads the compiled data model from the Cube REST API: the cubes and the
views with their measures, dimensions and segments, and the SQL that Cube
generates for each measure and each dimension. Cube compiles that SQL and does
not run it. synq-cube reads no data rows.

Run it beside a Cube deployment that Coalesce Quality cannot reach.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Safe in the struct literal: `build` is a package-level variable, so Go
		// initializes it before this literal.
		Version: build.String(),
	}
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	deployment := region.Register(root)
	root.AddCommand(newUploadCommand(deps, deployment))
	return root
}

// Execute runs the command line and sets the exit status.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	root := NewRootCommand(Deps{Stdout: os.Stdout, Stderr: os.Stderr})
	err := root.ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
