// Package region registers the two flags that select the Coalesce Quality deployment:
// --region and --endpoint.
//
// The region table and the precedence of the sources are in
// github.com/getsynq/quality-oauth-go. This package adds only the cobra flags.
package region

import (
	"fmt"
	"strings"

	qualityoauth "github.com/getsynq/quality-oauth-go"
	"github.com/spf13/cobra"
)

// Flag names.
const (
	FlagRegion   = "region"
	FlagEndpoint = "endpoint"
)

// Flags holds the values of the two flags and resolves them into a target.
type Flags struct {
	region   string
	endpoint string
}

// Register adds --region and --endpoint to the persistent flags of cmd, so the two flags work
// on each subcommand. Call it on the root command.
//
// --region is checked when it is parsed and completes from the region table. The check that two
// flags name different deployments runs in the PersistentPreRun of the root, so it runs for
// each command.
func Register(cmd *cobra.Command) *Flags {
	f := &Flags{}

	flags := cmd.PersistentFlags()
	flags.Var(&regionValue{into: &f.region}, FlagRegion,
		"Coalesce Quality deployment to use: "+strings.Join(qualityoauth.RegionNames(), ", ")+" (or $"+qualityoauth.RegionEnvVar+")")
	flags.StringVar(&f.endpoint, FlagEndpoint, "",
		"Coalesce Quality API endpoint, for staging or a self-hosted deployment; overrides --"+
			FlagRegion+" (or $"+qualityoauth.EndpointEnvVar+")")

	_ = cmd.RegisterFlagCompletionFunc(FlagRegion,
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return qualityoauth.RegionNames(), cobra.ShellCompDirectiveNoFileComp
		})
	chainPersistentPreRun(cmd, f.Validate)
	return f
}

// chainPersistentPreRun makes check run before the pre-run hook that the command already has.
// Cobra runs only the closest hook and prefers the form that returns an error, so the new hook
// calls the old one.
func chainPersistentPreRun(cmd *cobra.Command, check func() error) {
	existingE, existing := cmd.PersistentPreRunE, cmd.PersistentPreRun
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if err := check(); err != nil {
			return err
		}
		if existingE != nil {
			return existingE(c, args)
		}
		if existing != nil {
			existing(c, args)
		}
		return nil
	}
}

// Region is the --region value. It is empty if the flag was not given.
func (f *Flags) Region() string { return f.region }

// Endpoint is the --endpoint value. It is empty if the flag was not given.
func (f *Flags) Endpoint() string { return f.endpoint }

// Validate rejects a command line that names two different deployments.
func (f *Flags) Validate() error {
	_, err := qualityoauth.ResolveTarget(f.region, f.endpoint)
	return err
}

// Target resolves the deployment: the flags, then the environment, then the last login, then
// the default region.
func (f *Flags) Target() (qualityoauth.Target, error) {
	return qualityoauth.Sources{RegionFlag: f.region, EndpointFlag: f.endpoint}.Resolve()
}

// regionValue checks --region when it is parsed, so an unknown name fails on each command and
// the error names the valid set.
type regionValue struct{ into *string }

func (v *regionValue) String() string {
	if v.into == nil {
		return ""
	}
	return *v.into
}

func (v *regionValue) Set(name string) error {
	if _, ok := qualityoauth.LookupRegion(name); !ok {
		return fmt.Errorf("unknown region %q, expected one of %s", name, strings.Join(qualityoauth.RegionNames(), ", "))
	}
	*v.into = name
	return nil
}

// Type names the placeholder in `--region region` in the help output.
func (v *regionValue) Type() string { return "region" }
