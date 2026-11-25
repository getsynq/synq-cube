package region_test

import (
	"bytes"
	"testing"

	qualityoauth "github.com/getsynq/quality-oauth-go"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/getsynq/synq-cube/internal/region"
)

// newTree builds a root with one subcommand: the flags are registered on the root and must
// reach each command under it.
func newTree(t *testing.T) (*cobra.Command, *cobra.Command, *region.Flags) {
	t.Helper()
	root := &cobra.Command{Use: "tool", SilenceUsage: true, SilenceErrors: true}
	sub := &cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(sub)
	flags := region.Register(root)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root, sub, flags
}

func TestBothFlagsWorkOnEverySubcommand(t *testing.T) {
	root, _, flags := newTree(t)

	root.SetArgs([]string{"run", "--region", "us"})
	require.NoError(t, root.Execute())
	require.Equal(t, "us", flags.Region())

	target, err := flags.Target()
	require.NoError(t, err)
	require.Equal(t, "api.us.synq.io:443", target.Endpoint)

	root, _, flags = newTree(t)
	root.SetArgs([]string{"run", "--endpoint", "developer.synq.dev:443"})
	require.NoError(t, root.Execute())
	require.Equal(t, "developer.synq.dev:443", flags.Endpoint())
	target, err = flags.Target()
	require.NoError(t, err)
	require.Equal(t, "developer.synq.dev:443", target.Endpoint)
	require.Empty(t, target.Region)
}

func TestUnknownRegionFailsAtParseTimeNamingTheValidSet(t *testing.T) {
	root, _, _ := newTree(t)
	root.SetArgs([]string{"run", "--region", "mars"})

	err := root.Execute()
	require.Error(t, err)
	require.ErrorContains(t, err, `unknown region "mars"`)
	for _, name := range qualityoauth.RegionNames() {
		require.ErrorContains(t, err, name)
	}
}

func TestRegionCompletesFromTheRegionTable(t *testing.T) {
	root, _, _ := newTree(t)
	out := &bytes.Buffer{}
	root.SetOut(out)
	// __complete is the hidden command that a shell calls for a completion.
	root.SetArgs([]string{cobra.ShellCompRequestCmd, "run", "--region", ""})
	require.NoError(t, root.Execute())

	for _, name := range qualityoauth.RegionNames() {
		require.Contains(t, out.String(), name)
	}
}

func TestContradictingFlagsFailBeforeTheCommandRuns(t *testing.T) {
	root, sub, _ := newTree(t)
	ran := false
	sub.RunE = func(*cobra.Command, []string) error { ran = true; return nil }
	root.SetArgs([]string{"run", "--region", "us", "--endpoint", "developer.synq.io:443"})

	err := root.Execute()
	require.Error(t, err)
	require.ErrorContains(t, err, "pass only one")
	require.False(t, ran)
}

func TestRegisterKeepsTheCommandsOwnPreRunHook(t *testing.T) {
	root := &cobra.Command{Use: "tool", SilenceUsage: true, SilenceErrors: true}
	setUp := false
	root.PersistentPreRun = func(*cobra.Command, []string) { setUp = true }
	root.AddCommand(&cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error { return nil }})
	region.Register(root)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"run", "--region", "us"})

	require.NoError(t, root.Execute())
	require.True(t, setUp, "the setup of the command itself must still run")
}
