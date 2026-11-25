package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	qualityoauth "github.com/getsynq/quality-oauth-go"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/internal/quality"
	"github.com/getsynq/synq-cube/internal/region"
)

// Environment variables of the Cube side. The secrets have no flag, so they do not show in the
// process list.
const (
	envCubeAPIURL               = "CUBE_API_URL"
	envCubeAPISecret            = "CUBE_API_SECRET"
	envCubeAPIToken             = "CUBE_API_TOKEN"
	envCubeSecurityContext      = "CUBE_SECURITY_CONTEXT"
	envCubeSecurityContextLabel = "CUBE_SECURITY_CONTEXT_LABEL"
	envCubeVersion              = "CUBE_VERSION"
	envCubeCloudURL             = "CUBE_CLOUD_URL"
	envCubeCloudAPIKey          = "CUBE_CLOUD_API_KEY"
	envCubeCloudDeploymentID    = "CUBE_CLOUD_DEPLOYMENT_ID"
	envCubeCloudEnvironmentID   = "CUBE_CLOUD_ENVIRONMENT_ID"
	envCubeInclude              = "CUBE_INCLUDE"
	envCubeExclude              = "CUBE_EXCLUDE"
	envCubeSkipHidden           = "CUBE_SKIP_HIDDEN"
	envCubeSkipMemberSQL        = "CUBE_SKIP_MEMBER_SQL"
	envCubeSkipModelExport      = "CUBE_SKIP_MODEL_EXPORT"
)

// Names of the flags that the command reads back.
const (
	flagInput           = "input"
	flagInclude         = "include"
	flagExclude         = "exclude"
	flagSkipHidden      = "skip-hidden"
	flagSkipMemberSQL   = "skip-member-sql"
	flagSkipModelExport = "skip-model-export"
)

// The flags that set how the command reads Cube. With --input the command does not read Cube,
// so it refuses them.
var collectionFlags = []string{
	"cube-api-url", "cube-security-context", "security-context-label", "cube-version",
	"cube-cloud-url", "cube-cloud-deployment-id", "cube-cloud-environment-id",
	"concurrency", "request-timeout",
}

// stdinName is the value of --input that reads the standard input.
const stdinName = "-"

// How many warnings the command prints before it prints only their count.
const maxPrintedWarnings = 20

type uploadFlags struct {
	cubeAPIURL             string
	securityContext        string
	securityContextLabel   string
	cubeVersion            string
	cubeCloudURL           string
	cubeCloudDeploymentID  string
	cubeCloudEnvironmentID string
	concurrency            int
	requestTimeout         time.Duration
	include                []string
	exclude                []string
	skipHidden             bool
	skipMemberSQL          bool
	skipModelExport        bool
	input                  string
	dryRun                 bool
	output                 string
}

func newUploadCommand(deps Deps, deployment *region.Flags) *cobra.Command {
	flags := &uploadFlags{}
	command := &cobra.Command{
		Use:   "upload",
		Short: "Read the data model from Cube and send one snapshot to Coalesce Quality",
		Long: `Read the data model from Cube and send one snapshot to Coalesce Quality.

The command sends two kinds of request to the Cube REST API:

  GET /v1/meta?extended   the compiled data model
  GET /v1/sql             the SQL of one measure or one dimension, one request for each

Cube compiles the SQL and does not run it. The command never requests /v1/load.

Credential of Cube, the first one that is set:

  ` + envCubeCloudAPIKey + `   an API key of Cube Cloud, with ` + envCubeCloudURL + ` and ` + envCubeCloudDeploymentID + `
  ` + envCubeAPIToken + `       a JSON Web Token that you made
  ` + envCubeAPISecret + `      the API secret of the deployment (CUBEJS_API_SECRET). The command
                       signs a short-lived token with it and puts ` + envCubeSecurityContext + ` in it.

Credential of Coalesce Quality:

  ` + qualityoauth.TokenEnvVar + `        the token of the Cube integration, from its settings page

The integration names the deployment, so the command has no flag for it.

A member for which Cube generates no SQL is a warning. The command fails if the
data model cannot be read or if the upload fails.

Snapshot file:

  --output writes the snapshot to a file. The file is the protojson form of the
  message synq.ingest.cube.v1.IngestSnapshotRequest. --input sends a snapshot
  from such a file. The command then sends no request to Cube and needs no
  credential of Cube.

Filters:

  --include, --exclude, --skip-hidden, --skip-member-sql and --skip-model-export
  leave parts of the data model out of the snapshot. They work on a collection
  and on a file. For Coalesce Quality a snapshot is the whole data model: a cube
  or a view that is not in the snapshot is removed from Coalesce Quality. A
  filter is not a way to update a part of the data model.`,
		Example: `  # Cube Core or a self-hosted Cube, with the API secret
  export CUBE_API_URL=https://cube.example.com/cubejs-api
  export CUBE_API_SECRET=...
  export QUALITY_TOKEN=...
  synq-cube upload

  # Write the snapshot to a file and send nothing
  synq-cube upload --dry-run --output snapshot.json

  # Send the snapshot of a file
  synq-cube upload --input snapshot.json

  # Leave the cubes and the views with these names out of the snapshot
  synq-cube upload --exclude 'tmp_*' --exclude 'scratch_*'`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runUpload(command.Context(), deps, deployment, flags, command.Flags().Changed)
		},
	}
	f := command.Flags()
	f.StringVar(&flags.cubeAPIURL, "cube-api-url", "",
		"Base URL of the Cube REST API with the base path, for example https://cube.example.com/cubejs-api (or $"+envCubeAPIURL+")")
	f.StringVar(&flags.securityContext, "cube-security-context", "",
		"Security context of the token that the command makes, as a JSON object. Not used with "+envCubeAPIToken+" (or $"+envCubeSecurityContext+")")
	f.StringVar(&flags.securityContextLabel, "security-context-label", "",
		"A name for the security context, for example full-read. The security context itself is never sent (or $"+
			envCubeSecurityContextLabel+")")
	f.StringVar(&flags.cubeVersion, "cube-version", "", "Version of Cube, for example 1.3.30 (or $"+envCubeVersion+")")
	f.StringVar(&flags.cubeCloudURL, "cube-cloud-url", "",
		"URL of the Cube Cloud account, for example https://example.cubecloud.dev (or $"+envCubeCloudURL+")")
	f.StringVar(&flags.cubeCloudDeploymentID, "cube-cloud-deployment-id", "",
		"Id of the Cube Cloud deployment, the number in its URL (or $"+envCubeCloudDeploymentID+")")
	f.StringVar(&flags.cubeCloudEnvironmentID, "cube-cloud-environment-id", "",
		"Id of the Cube Cloud environment. The default is the production environment (or $"+envCubeCloudEnvironmentID+")")
	f.IntVar(&flags.concurrency, "concurrency", cube.DefaultConcurrency, "How many /v1/sql requests run at the same time")
	f.DurationVar(&flags.requestTimeout, "request-timeout", cube.DefaultRequestTimeout, "Time limit of one request to Cube")
	f.StringArrayVar(&flags.include, flagInclude, nil,
		"Keep only the cubes and the views with a name that matches this glob pattern, for example 'orders*'. "+
			"Can be set more than once. Coalesce Quality removes each cube and each view that is not in the snapshot "+
			"(or $"+envCubeInclude+", separated by commas)")
	f.StringArrayVar(&flags.exclude, flagExclude, nil,
		"Leave out the cubes and the views with a name that matches this glob pattern. It wins over --include. "+
			"Can be set more than once. Coalesce Quality removes each cube and each view that is not in the snapshot "+
			"(or $"+envCubeExclude+", separated by commas)")
	f.BoolVar(&flags.skipHidden, flagSkipHidden, false,
		"Leave out the cubes, the views and the members that are not public. "+
			"Coalesce Quality removes each cube and each view that is not in the snapshot (or $"+envCubeSkipHidden+")")
	f.BoolVar(&flags.skipMemberSQL, flagSkipMemberSQL, false,
		"Leave out the SQL of each member and send no /v1/sql request. The snapshot then has no lineage to the warehouse tables and no data source for each cube (or $"+
			envCubeSkipMemberSQL+")")
	f.BoolVar(&flags.skipModelExport, flagSkipModelExport, false,
		"Leave out the Cube Cloud model export and send no request for it (or $"+envCubeSkipModelExport+")")
	f.StringVar(&flags.input, flagInput, "",
		"Send the snapshot of this file and send no request to Cube. Use - for the standard input")
	f.BoolVar(&flags.dryRun, "dry-run", false, "Send nothing to Coalesce Quality. Without --input, use it with --output")
	f.StringVar(&flags.output, "output", "", "Write the snapshot to this file as JSON")
	return command
}

func runUpload(ctx context.Context, deps Deps, deployment *region.Flags, flags *uploadFlags, changed func(string) bool) error {
	fromFile := flags.input != ""
	if fromFile {
		for _, name := range collectionFlags {
			if changed(name) {
				return fmt.Errorf("--%s sets how the command reads Cube, and with --input the command does not read Cube: remove --%s", name, name)
			}
		}
	}
	if flags.dryRun && flags.output == "" && !fromFile {
		return errors.New("--dry-run sends nothing: set --output to the file for the snapshot")
	}
	if flags.concurrency < 1 {
		return errors.New("--concurrency must be 1 or more")
	}
	if flags.requestTimeout <= 0 {
		return errors.New("--request-timeout must be more than 0")
	}
	filter, err := newFilter(flags, changed)
	if err != nil {
		return err
	}

	// The two sources of a snapshot. Each fails here for a wrong setting, before the connection
	// to Coalesce Quality.
	var client *cube.Client
	var fileSnapshot *cube.Snapshot
	if fromFile {
		fileSnapshot, err = readSnapshot(deps, flags.input)
	} else {
		client, err = newCubeClient(ctx, flags)
	}
	if err != nil {
		return err
	}

	// Connect before the collection, so a wrong token fails before the requests to Cube.
	var uploader *quality.Uploader
	var target qualityoauth.Target
	if !flags.dryRun {
		target, err = deployment.Target()
		if err != nil {
			return err
		}
		uploader, err = quality.NewUploader(ctx, target, qualityoauth.Getenv(qualityoauth.TokenEnvVar), deps.QualityOptions...)
		if err != nil {
			return err
		}
		defer func() { _ = uploader.Close() }()
	}

	var snapshot *cube.Snapshot
	var report *cube.Report
	if fromFile {
		fmt.Fprintf(deps.Stdout, "Reading the snapshot from %s\n", inputName(flags.input))
		snapshot, report, err = filterSnapshot(fileSnapshot, filter)
		if err != nil {
			return fmt.Errorf("cannot use the snapshot of %s: %w", inputName(flags.input), err)
		}
	} else {
		fmt.Fprintf(deps.Stdout, "Reading the data model from %s\n", client.APIURL())
		snapshot, report, err = cube.Collect(ctx, client, cube.Options{
			Concurrency:          flags.concurrency,
			SecurityContextLabel: valueOrEnv(flags.securityContextLabel, envCubeSecurityContextLabel),
			CubeVersion:          valueOrEnv(flags.cubeVersion, envCubeVersion),
			Filter:               filter,
		})
		if err != nil {
			return fmt.Errorf("cannot read the data model: %w", err)
		}
	}
	printReport(deps, report, filter)

	// The collector returns a plain struct. The message of the upload is built here, with the
	// build stamps of this binary. A snapshot from a file gets them too.
	request := quality.NewRequest(snapshot, build.Version, build.Time)

	if flags.output != "" {
		data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(request)
		if err != nil {
			return fmt.Errorf("cannot write the snapshot: %w", err)
		}
		if err := os.WriteFile(flags.output, append(data, '\n'), 0o600); err != nil {
			return fmt.Errorf("cannot write the snapshot: %w", err)
		}
		fmt.Fprintf(deps.Stdout, "Wrote the snapshot to %s\n", flags.output)
	}
	if flags.dryRun {
		fmt.Fprintln(deps.Stdout, "Dry run: nothing was sent to Coalesce Quality")
		return nil
	}

	if err := uploader.Upload(ctx, request); err != nil {
		return fmt.Errorf("the upload to Coalesce Quality, %s, failed: %w", target, err)
	}
	fmt.Fprintf(deps.Stdout, "Sent the snapshot to Coalesce Quality, %s\n", target)
	return nil
}

// newFilter reads the filter from the flags and the environment. A flag wins over its variable.
// It fails for a pattern that is not valid, before the command sends a request.
func newFilter(flags *uploadFlags, changed func(string) bool) (cube.Filter, error) {
	filter := cube.Filter{Include: flags.include, Exclude: flags.exclude}
	if !changed(flagInclude) {
		filter.Include = splitList(os.Getenv(envCubeInclude))
	}
	if !changed(flagExclude) {
		filter.Exclude = splitList(os.Getenv(envCubeExclude))
	}
	for _, toggle := range []struct {
		flag  string
		env   string
		value bool
		into  *bool
	}{
		{flagSkipHidden, envCubeSkipHidden, flags.skipHidden, &filter.SkipHidden},
		{flagSkipMemberSQL, envCubeSkipMemberSQL, flags.skipMemberSQL, &filter.SkipMemberSQL},
		{flagSkipModelExport, envCubeSkipModelExport, flags.skipModelExport, &filter.SkipModelExport},
	} {
		*toggle.into = toggle.value
		text := os.Getenv(toggle.env)
		if changed(toggle.flag) || text == "" {
			continue
		}
		value, err := strconv.ParseBool(text)
		if err != nil {
			return cube.Filter{}, fmt.Errorf("%s is %q: set it to true or false", toggle.env, text)
		}
		*toggle.into = value
	}
	if err := filter.Validate(); err != nil {
		return cube.Filter{}, err
	}
	return filter, nil
}

// splitList reads a list that is separated by commas. It drops the empty entries.
func splitList(text string) []string {
	var out []string
	for _, entry := range strings.Split(text, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

func inputName(input string) string {
	if input == stdinName {
		return "the standard input"
	}
	return input
}

// readSnapshot reads a snapshot file: the protojson form of IngestSnapshotRequest, as --output
// writes it. A field that the message does not have is an error, so a typing error in a file
// that a person edited does not pass. The uploader fields of the file are not used: the command
// sends its own.
func readSnapshot(deps Deps, input string) (*cube.Snapshot, error) {
	var data []byte
	var err error
	if input == stdinName {
		stdin := deps.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(input)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the snapshot: %w", err)
	}
	request := &ingestcubev1.IngestSnapshotRequest{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, request); err != nil {
		return nil, fmt.Errorf("%s is not a snapshot file: %w", inputName(input), err)
	}
	if len(request.GetMeta()) == 0 {
		return nil, fmt.Errorf("%s is not a snapshot file: it has no \"meta\"", inputName(input))
	}
	if !hasCubesList(request.GetMeta()) {
		return nil, fmt.Errorf("%s is not a snapshot file: \"meta\" is not a JSON object with a \"cubes\" list", inputName(input))
	}
	if request.GetCollectedAt() == nil || request.GetCollectedAt().CheckValid() != nil || request.GetCollectedAt().AsTime().Unix() <= 0 {
		return nil, fmt.Errorf("%s is not a snapshot file: it has no \"collectedAt\"", inputName(input))
	}
	return quality.NewSnapshot(request), nil
}

// filterSnapshot applies the filter to a snapshot from a file and counts what the result holds.
func filterSnapshot(snapshot *cube.Snapshot, filter cube.Filter) (*cube.Snapshot, *cube.Report, error) {
	filtered, filterReport, err := filter.Apply(snapshot)
	if err != nil {
		return nil, nil, err
	}
	report, err := cube.Describe(filtered)
	if err != nil {
		return nil, nil, err
	}
	report.LeftOut = filterReport.LeftOut
	report.Warnings = filterReport.Warnings
	return filtered, report, nil
}

func newCubeClient(ctx context.Context, flags *uploadFlags) (*cube.Client, error) {
	apiURL := valueOrEnv(flags.cubeAPIURL, envCubeAPIURL)
	securityContext := valueOrEnv(flags.securityContext, envCubeSecurityContext)
	options := []cube.ClientOption{cube.WithRequestTimeout(flags.requestTimeout)}

	var tokens cube.TokenSource
	switch {
	case os.Getenv(envCubeCloudAPIKey) != "":
		deploymentID, err := parseID(valueOrEnv(flags.cubeCloudDeploymentID, envCubeCloudDeploymentID), "the Cube Cloud deployment id", false)
		if err != nil {
			return nil, err
		}
		environmentID, err := parseID(valueOrEnv(flags.cubeCloudEnvironmentID, envCubeCloudEnvironmentID), "the Cube Cloud environment id", true)
		if err != nil {
			return nil, err
		}
		cloud, err := cube.NewCloudTokenSource(cube.CloudConfig{
			URL:             valueOrEnv(flags.cubeCloudURL, envCubeCloudURL),
			APIKey:          os.Getenv(envCubeCloudAPIKey),
			DeploymentID:    deploymentID,
			EnvironmentID:   environmentID,
			SecurityContext: securityContext,
		})
		if err != nil {
			return nil, err
		}
		if apiURL == "" {
			apiURL, err = cloud.APIURL(ctx)
			if err != nil {
				return nil, fmt.Errorf("cannot find the REST API URL of the Cube Cloud environment, set %s: %w", envCubeAPIURL, err)
			}
		}
		tokens = cloud
	case os.Getenv(envCubeAPIToken) != "":
		static, err := cube.NewStaticTokenSource(os.Getenv(envCubeAPIToken))
		if err != nil {
			return nil, err
		}
		tokens = static
	case os.Getenv(envCubeAPISecret) != "":
		signed, err := cube.NewAPISecretTokenSource(os.Getenv(envCubeAPISecret), securityContext)
		if err != nil {
			return nil, err
		}
		tokens = signed
	default:
		return nil, fmt.Errorf("no credential of Cube is set: set %s, %s or %s", envCubeAPISecret, envCubeAPIToken, envCubeCloudAPIKey)
	}
	if apiURL == "" {
		return nil, fmt.Errorf("the URL of the Cube REST API is not set: set --cube-api-url or %s", envCubeAPIURL)
	}
	return cube.NewClient(apiURL, tokens, options...)
}

func printReport(deps Deps, report *cube.Report, filter cube.Filter) {
	fmt.Fprintf(deps.Stdout, "Cubes: %d\n", report.Cubes)
	fmt.Fprintf(deps.Stdout, "Views: %d\n", report.Views)
	fmt.Fprintf(deps.Stdout, "Members: %d\n", report.Members)
	fmt.Fprintf(deps.Stdout, "Members with an error: %d\n", report.MemberErrors)
	if report.ModelExport {
		fmt.Fprintln(deps.Stdout, "Cube Cloud model export: yes")
	}
	if len(filter.Include)+len(filter.Exclude) > 0 {
		fmt.Fprintf(deps.Stdout, "Left out by --include and --exclude: cubes %d, views %d, members %d\n",
			report.ExcludedCubes, report.ExcludedViews, report.ExcludedMembers)
	}
	if filter.SkipHidden {
		fmt.Fprintf(deps.Stdout, "Left out by --skip-hidden: cubes %d, views %d, members %d\n",
			report.HiddenCubes, report.HiddenViews, report.HiddenMembers)
	}
	if report.MemberSQLSkipped {
		fmt.Fprintln(deps.Stdout, "Left out by --skip-member-sql: the SQL of each member")
	}
	switch {
	case report.ModelExportSkipped && filter.SkipModelExport:
		fmt.Fprintln(deps.Stdout, "Left out by --skip-model-export: the Cube Cloud model export")
	case report.ModelExportSkipped:
		fmt.Fprintln(deps.Stdout, "Left out: the Cube Cloud model export. It cannot be filtered, and a filter removed a part of the data model")
	}
	printWarnings(deps.Stderr, report.Warnings)
}

func printWarnings(w io.Writer, warnings []string) {
	for i, warning := range warnings {
		if i == maxPrintedWarnings {
			fmt.Fprintf(w, "warning: %d more warnings are not shown\n", len(warnings)-maxPrintedWarnings)
			return
		}
		fmt.Fprintf(w, "warning: %s\n", strings.ReplaceAll(warning, "\n", " "))
	}
}

func valueOrEnv(value string, env string) string {
	if value != "" {
		return value
	}
	return os.Getenv(env)
}

func parseID(text string, what string, optional bool) (int64, error) {
	if text == "" {
		if optional {
			return 0, nil
		}
		return 0, fmt.Errorf("%s is not set", what)
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s %q is not a positive number", what, text)
	}
	return id, nil
}

// hasCubesList is true if the text is a JSON object with a `cubes` list.
func hasCubesList(meta []byte) bool {
	var object struct {
		Cubes *[]json.RawMessage `json:"cubes"`
	}
	return json.Unmarshal(meta, &object) == nil && object.Cubes != nil
}
