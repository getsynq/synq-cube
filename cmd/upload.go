package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
)

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
data model cannot be read or if the upload fails.`,
		Example: `  # Cube Core or a self-hosted Cube, with the API secret
  export CUBE_API_URL=https://cube.example.com/cubejs-api
  export CUBE_API_SECRET=...
  export QUALITY_TOKEN=...
  synq-cube upload

  # Write the snapshot to a file and send nothing
  synq-cube upload --dry-run --output snapshot.json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runUpload(command.Context(), deps, deployment, flags)
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
	f.BoolVar(&flags.dryRun, "dry-run", false, "Read the data model and send nothing. Use it with --output")
	f.StringVar(&flags.output, "output", "", "Write the snapshot to this file as JSON")
	return command
}

func runUpload(ctx context.Context, deps Deps, deployment *region.Flags, flags *uploadFlags) error {
	if flags.dryRun && flags.output == "" {
		return errors.New("--dry-run sends nothing: set --output to the file for the snapshot")
	}
	if flags.concurrency < 1 {
		return errors.New("--concurrency must be 1 or more")
	}
	if flags.requestTimeout <= 0 {
		return errors.New("--request-timeout must be more than 0")
	}

	client, err := newCubeClient(ctx, flags)
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

	fmt.Fprintf(deps.Stdout, "Reading the data model from %s\n", client.APIURL())
	snapshot, report, err := cube.Collect(ctx, client, cube.Options{
		Concurrency:          flags.concurrency,
		SecurityContextLabel: valueOrEnv(flags.securityContextLabel, envCubeSecurityContextLabel),
		CubeVersion:          valueOrEnv(flags.cubeVersion, envCubeVersion),
	})
	if err != nil {
		return fmt.Errorf("cannot read the data model: %w", err)
	}
	printReport(deps, report)
	// The collector returns a plain struct. The message of the upload is built here, with the
	// build stamps of this binary.
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

// newCubeClient picks the credential of Cube: Cube Cloud, then a ready token, then the API secret.
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

func printReport(deps Deps, report *cube.Report) {
	fmt.Fprintf(deps.Stdout, "Cubes: %d\n", report.Cubes)
	fmt.Fprintf(deps.Stdout, "Views: %d\n", report.Views)
	fmt.Fprintf(deps.Stdout, "Members: %d\n", report.Members)
	fmt.Fprintf(deps.Stdout, "Members with an error: %d\n", report.MemberErrors)
	if report.ModelExport {
		fmt.Fprintln(deps.Stdout, "Cube Cloud model export: yes")
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
