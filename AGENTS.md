# AGENTS.md: changing synq-cube, the Cube uploader

`synq-cube` is a command-line tool. It reads the data model of a Cube deployment from the Cube REST API. It sends the data model to Coalesce Quality as one snapshot (`synq.ingest.cube.v1.CubeService/IngestSnapshot`).

This repository is public. Customers read the source to audit the tool, and they change it. Usage is in [`README.md`](README.md).

## Layout

| Path | Content |
|---|---|
| `main.go`, `cmd/` | The command tree: the settings, the output and the call of the upload |
| `cube/` | The collector: the Cube REST client, the token sources and `Collect` |
| `cube/cubemeta/` | The reader of the `/v1/meta` response |
| `cube/cubetest/` | A test server that replays a recorded Cube deployment |
| `internal/quality/` | The upload client for the Coalesce Quality API |
| `internal/buildinfo/` | The build stamps |
| `internal/region/` | The `--region` and `--endpoint` flags |

## Commands

```bash
go build ./... && echo OK
go test ./...
gofmt -l .
go vet ./...
```

`gofmt -l .` must print nothing.

## One collector, two callers

The package `github.com/getsynq/synq-cube/cube` has two callers: this tool and the Coalesce Quality backend. The two callers send the same snapshot.

- Put each change of what a snapshot holds in `cube/`, never in `cmd/`.
- Keep the exported API of `cube/` stable: `Client`, `NewClient`, `NewStaticTokenSource`, `NewAPISecretTokenSource`, `NewCloudTokenSource`, `Collect`, `Describe`, `Options`, `Report`, `Snapshot`, `MemberSQL`, `DataSource`, `DataSourcesFromEnv`, `ParseDataSource`, `Filter`, `Filter.Apply`, `FilterReport`, `LeftOut`, `ErrNothingLeft`, `cubemeta` and `cubetest.NewServer`. If you must change a signature, say so in the pull request.
- Do not import `github.com/spf13/cobra` or a package under `internal/` from `cube/`.
- Do not read environment variables in `cube/`. The caller gives each setting as a parameter.
- Keep the dependencies of `cube/` small: the standard library, `github.com/golang-jwt/jwt/v5`, `golang.org/x/sync` and, in tests, `github.com/stretchr/testify`.

### No proto package in the collector

`Collect` returns the plain struct `cube.Snapshot`. It does not return the proto message.

- Do not import a generated proto package in `cube/`, `cube/cubemeta/` or `cube/cubetest/`, also not in a test. Do not import `google.golang.org/protobuf` or `google.golang.org/grpc` there.
- Reason: the backend has its own generated copy of the proto package `synq.ingest.cube.v1`. If one binary holds two Go packages that register the same proto package, the binary panics at start.
- The job in `.github/workflows/go.yml` fails if `go list -deps -test ./cube/...` shows a proto package.
- The fields of `cube.Snapshot` and `cube.MemberSQL` mirror the messages `IngestSnapshotRequest` and `MemberSql`. An optional field of the message is an empty value in the struct. The doc comment of each field says what empty means.
- `internal/quality/request.go` is the one place that maps a `cube.Snapshot` to the message (`quality.NewRequest`) and back (`quality.NewSnapshot`). `NewRequest` adds `uploader_version` and `uploader_build_time`.
- If you add a field to `cube.Snapshot` or `cube.MemberSQL`, add it to `quality.NewRequest`, to `quality.NewSnapshot` and to `TestNewRequestMapsEachField`. That test fails for a field that has no value in it.

### The filter

`cube.Filter` leaves parts of the data model out of a snapshot. It has two entries: `Collect` applies `Options.Filter` while it reads Cube, and `Filter.Apply` applies a filter to a snapshot that exists.

- The two entries give the same snapshot. `TestCollectWithAFilterEqualsApplyOnTheWholeSnapshot` asserts it. If you add a setting to `cube.Filter`, add it to the two entries and to that test.
- `Collect` sends no `/v1/sql` request for a member that the filter leaves out.
- The filter never decodes `Meta` into typed structs. `rewriteArrays` in `cube/filter.go` writes only the lists that change, from the JSON text of the entries that stay. A property that the tool does not know keeps its bytes.
- A snapshot is the whole data model for Coalesce Quality: a cube or a view that is not in the snapshot is removed there. Say this in the help text of each setting that removes a cube or a view.
- Give each new setting a flag and a `CUBE_*` variable in `cmd/upload.go`, a row in the README and a line in the run summary (`printReport`).

### The data sources

`Snapshot.DataSources` says what each data source of Cube is, so that Coalesce Quality can find its warehouse. The order of precedence for one name is: `Options.DataSources` (the `--data-source` setting), Cube Cloud (`GET /v1/data-sources`), `Options.EnvironmentDataSources` (the variables of Cube).

- `cube.DataSourcesFromEnv` reads only the variables in `envSuffixes` in `cube/datasource.go`. None of them is a credential. Do not add a variable that holds a user, a password, a key, a token, a secret or a credentials file. `TestFromEnvReadsNoCredential` feeds such variables and asserts that no value is in the result.
- `cube.DataSource` has no field for a credential. Do not add one.
- `Collect` reads no environment variable. The CLI calls `DataSourcesFromEnv(os.Environ())` and gives the result in the options.
- `Collect` keeps `default` and the data sources that the members use. `Filter.Apply` keeps the list as it is.
- A failed read of Cube Cloud is a warning, never an error.

### The snapshot file

`--output` writes the protojson form of `IngestSnapshotRequest`, and `--input` reads it. This is the one file format of the tool.

- Read the file with `DiscardUnknown: false`, so a typing error in a file that a person edited is an error.
- `quality.NewSnapshot` maps the message of a file to a `cube.Snapshot`. The filter and the counts then work on the snapshot, as after a collection.
- The tool sends its own `uploader_version` and `uploader_build_time`, never the values of the file.
- With `--input` the tool sends no request to Cube. It refuses the flags that set how it reads Cube (`collectionFlags`). Add a new collection flag to that list.

## What the tool must never do

- Do not read a credential of a data source of Cube (`CUBEJS_DB_USER`, `CUBEJS_DB_PASS` and the others). See § The data sources.
- Do not call `/v1/load`. The promise to the customer is that no data rows leave the network. `TestSnapshotOfTheRecording` in `cube/` asserts the list of request paths.
- Do not send or print the security context, `CUBE_API_SECRET`, `CUBE_API_TOKEN`, `CUBE_CLOUD_API_KEY` or `QUALITY_TOKEN`. A secret has an environment variable and no flag.
- Do not add a `--deployment` flag. The integration behind `QUALITY_TOKEN` names the deployment.
- Do not send data to a host other than the Cube deployment, Cube Cloud and the Coalesce Quality API. Do not add telemetry.

## Names in help text and docs

- The product name is "Coalesce Quality". The names `synq-cube`, `getsynq`, the `synq.io` domains and the proto package `synq.ingest.cube.v1` are identifiers and do not change.
- Read `QUALITY_TOKEN`, `QUALITY_REGION` and `QUALITY_API_ENDPOINT` with `qualityoauth.Getenv` or `internal/region`, never with `os.Getenv`. The older names of these variables are hidden aliases and must not show in help text or docs. `TestHelpUsesThePublicNames` checks the help text.
- The `CUBE_*` variables are the names of a Cube admin. Read them with `os.Getenv`, in `cmd/` only.
- Use the words of Cube: deployment, data model, cube, view, measure, dimension, segment, member, data source, pre-aggregation, security context.
- Write short sentences in the active voice. Do not use idioms.

## Public repository

- Do not write the name of a customer, a workspace or an internal project in code, tests, fixtures, commit messages or pull requests.
- The fixtures in `cube/cubetest/testdata` come from the public sample project `github.com/getsynq/cube-demo`. Record new fixtures only from a public sample project.
- Do not commit a snapshot from `--output`. It can hold a private data model. The `.gitignore` excludes `*.json` in the root.

## Authentication to Coalesce Quality

`QUALITY_TOKEN` is the long-lived token of a Cube integration. `internal/quality` exchanges it at `/oauth2/token` (password grant, user `synq`) for a short-lived access token. Do not send the integration token as a bearer token.

## Dependencies

- The proto messages and the gRPC client come from the buf generated SDK of the public API: `buf.build/gen/go/getsynq/api/protocolbuffers/go` and `buf.build/gen/go/getsynq/api/grpc/go`. The protos are at <https://buf.build/getsynq/api>.
- Only `internal/quality` and the tests of `cmd/` import them.
- Do not commit a `replace` directive for these modules. To build with a local copy, use a `go.work` file. Git ignores `go.work`.
- A version of a buf module holds two versions: the version of the plugin, then the date and the commit of the protos. Before you change a pin, check that the date of the protos does not go back.
- After a change of `go.mod`, run `GOWORK=off go mod tidy`, then `GOWORK=off go build ./... && echo OK`.

## Release

1. Push a tag `v*.*.*` on `main`. The workflow `.github/workflows/release.yaml` starts.
2. GoReleaser builds the archives and publishes the GitHub release of this repository.
3. The workflow builds the image from the same binaries and pushes it to `ghcr.io/getsynq/synq-cube`, with the version tag and `latest`. A prerelease tag (`v0.1.0-rc1`) does not move `latest`.

- The workflow uses only `GITHUB_TOKEN`. Do not add a secret.
- `internal/buildinfo` is the only home of the build stamps. `--version` and the `uploader_version` field of a snapshot read it.
- After a change of `.goreleaser.yml`, run `goreleaser release --snapshot --clean --skip=publish`. `goreleaser check` does not run the `before` hooks.
