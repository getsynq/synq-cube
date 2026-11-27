# AGENTS.md: changing synq-cube, the Cube uploader

`synq-cube` is a command-line tool. It reads the data model of a Cube deployment from the Cube REST API. It sends the data model to Coalesce Quality as one snapshot (`synq.ingest.cube.v1.CubeService/IngestSnapshot`).

This repository is public. Customers read the source to audit the tool, and they change it. Usage is in [`README.md`](README.md).

## Layout

| Path | Content |
|---|---|
| `main.go`, `cmd/` | The command tree: the settings, the output and the call of the upload |
| `cube/` | The collector: the Cube REST client, the token sources and `Collect` |
| `cube/cubemeta/` | The reader of the `/v1/meta` response and of the model export, and the merge of the two |
| `cube/cubetest/` | A test server that replays a recorded Cube deployment |
| `internal/quality/` | The upload client for the Coalesce Quality API |
| `internal/gitcontext/` | The reader of the git repository: the variables of a CI system and the git checkout |
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
- Keep the exported API of `cube/` stable: `Client`, `NewClient`, `NewStaticTokenSource`, `NewAPISecretTokenSource`, `NewCloudTokenSource`, `Collect`, `Describe`, `Options`, `Report`, `Snapshot`, `GeneratedSQL`, `GeneratedSQLMember`, `MemberError`, `GitContext`, `DataSource`, `DataSourcesFromEnv`, `ParseDataSource`, `Filter`, `Filter.Apply`, `FilterReport`, `LeftOut`, `ErrNothingLeft`, `CollectVariant`, `Variant`, `VariantReport`, `ValidateVariantLabel`, `ErrVariantModelExport`, `cubemeta` and `cubetest.NewServer`. If you must change a signature, say so in the pull request.
- Do not import `github.com/spf13/cobra` or a package under `internal/` from `cube/`.
- Do not read environment variables in `cube/`. The caller gives each setting as a parameter.
- Keep the dependencies of `cube/` small: the standard library, `github.com/golang-jwt/jwt/v5`, `golang.org/x/sync` and, in tests, `github.com/stretchr/testify`.

### No proto package in the collector

`Collect` returns the plain struct `cube.Snapshot`. It does not return the proto message.

- Do not import a generated proto package in `cube/`, `cube/cubemeta/` or `cube/cubetest/`, also not in a test. Do not import `google.golang.org/protobuf` or `google.golang.org/grpc` there.
- Reason: the backend has its own generated copy of the proto package `synq.ingest.cube.v1`. If one binary holds two Go packages that register the same proto package, the binary panics at start.
- The job in `.github/workflows/go.yml` fails if `go list -deps -test ./cube/...` shows a proto package.
- The fields of `cube.Snapshot`, `cube.GeneratedSQL`, `cube.MemberError` and `cube.GitContext` mirror the messages `IngestSnapshotRequest`, `GeneratedSql`, `MemberError` and `GitContext`. An optional field of the message is an empty value in the struct. The doc comment of each field says what empty means.
- `internal/quality/request.go` is the one place that maps a `cube.Snapshot` to the message (`quality.NewRequest`) and back (`quality.NewSnapshot`). `NewRequest` adds `uploader_version` and `uploader_build_time`.
- If you add a field to `cube.Snapshot` or to a struct in it, add it to `quality.NewRequest`, to `quality.NewSnapshot` and to `TestNewRequestMapsEachField`. That test fails for a field that has no value in it.
- The comments and the `buf.validate` rules of the proto file are the contract. `quality.NewRequest` sets no optional field to an empty value, always sets `ungrouped`, gives each member error a text, and writes the build time in RFC 3339 format or not at all. `TestACollectedSnapshotIsAValidMessage` validates the message of a collected snapshot with `buf.build/go/protovalidate`. If you change the collector or the mapping, keep that test green.

### The SQL of the members

`Collect` asks Cube for the SQL of many members in one `/v1/sql` request. The code is in `cube/batch.go`. The README says the steps in § How the tool reads the SQL.

- One batch holds the measures, or the dimensions, of one cube or of one view, and `Options.BatchSize` members at most. One entry of `Snapshot.GeneratedSQL` is the SQL of one request.
- A member is in one entry of `Snapshot.GeneratedSQL` or in `Snapshot.MemberErrors`, never in the two and never in two entries.
- Each member of an entry has its `Column`: the key of the member in `sql.aliasNameToMember` of the response, as Cube sent it. Do not build the column from the name of the member: a cube with `sql_alias` has another prefix. A member that has no column in a statement of many members is collected alone. If that response names no column, the member goes to `MemberErrors` (`errNoColumn`).
- `Snapshot.CollectedAt` is the time before the first request to Cube.
- Do not send a query of many members without `ungrouped` for a member whose ungrouped query did not fail when it was alone. A grouped query can read a pre-aggregation table, and then the member has no lineage to the tables of its cube.
- A member is alone in two requests at most: one ungrouped and one without `ungrouped`. Keep the count of requests low: Cube generates SQL in one process.
- `BatchSize: 1` gives one `GET /v1/sql` request for each member. `TestBatchSizeOneSendsOneRequestForEachMember` asserts it.
- A query of many members is a `POST /v1/sql` request (`Client.SQLOfMembers`), as a URL has a length limit. A query of one member is a `GET /v1/sql` request (`Client.SQL`).
- `cubetest.Server` answers a query of many members. Use `ErrorMembers`, `GroupedOnlyMembers`, `NoAliasMembers`, `NoAliases`, `NoPostSQL` and `MaxURLLength` to test a failure, and `SQLRequests` to assert the requests.
- `Report.SQLRequests` counts each `/v1/sql` request, with the requests that the client sent again.

### The model export and the private members

A Cube deployment in production returns only the public cubes, views and members from `/v1/meta`. The Cube Cloud model export (`POST /v1/model-export`) has each of them, also those with `public: false`. Cube Core does not serve it.

- `Collect` requests the model export before the SQL, and only with a `CloudTokenSource`. A failed request is a warning, never an error.
- `cubemeta.ParseModelExport` reads the model export. `ModelExport.OnlyInExport` lists what the model export has and `/v1/meta` does not have. `Collect` requests the SQL of those measures and dimensions in batches of their own, so a statement never mixes them with the members of `/v1/meta`.
- `Snapshot.DataModel` stays the response of `/v1/meta`, and `Snapshot.ModelExport` stays the response of the model export. Do not merge the two in the collector. Coalesce Quality adds what only the model export has, with `ModelExport.AddTo` of `cubemeta`.
- `cubemeta` is the only reader of the model export. The collector and Coalesce Quality use it, so the two agree on what is only in the model export. Do not read the model export in another package.
- `ModelExport.AddTo` gives each added cube, view and member the names of `/v1/meta` (`aggType`, `primaryKey`, `shortTitle`, `aliasMember`, `nestedFolders`, the `name` of a join). `/v1/meta` stays the source of each cube, view and member that it has.
- A snapshot has the SQL of a member that only the model export has only if it has the model export. If `Filter.Apply` removes the model export, it removes that SQL too.
- `Report.Cubes`, `Report.Views` and `Report.Members` include what only the model export has. `Report.ModelExportCubes`, `Report.ModelExportViews` and `Report.ModelExportMembers` count that part alone.
- The model export names a member with no cube (`revenue`), and its properties in snake case. `cubetest.ModelExport` and `cubetest.PublicMetaExtended` give the two responses of a deployment in production for the recording.

### The filter

`cube.Filter` leaves parts of the data model out of a snapshot. It has two entries: `Collect` applies `Options.Filter` while it reads Cube, and `Filter.Apply` applies a filter to a snapshot that exists.

- The two entries give the same snapshot, with one exception: the SQL text of a statement from which `Filter.Apply` removed a member. `Filter.Apply` keeps that text, and `Collect` asks Cube only for the members that stay. `TestCollectWithAFilterEqualsApplyOnTheWholeSnapshot` and `TestCollectWithAFilterEqualsApplyWithPrivateMembers` assert it. If you add a setting to `cube.Filter`, add it to the two entries and to that test.
- `Collect` puts no member that the filter leaves out in a `/v1/sql` request.
- The model export cannot be filtered. A filter that removes a cube, a view or a member, also one that only the model export has, removes the model export (`Filter.removesExportOnly`).
- `Filter.Apply` removes a member from the `Members` of its entry of `GeneratedSQL` and from `MemberErrors`. It removes an entry with no member left. It never changes the SQL text.
- The filter never decodes `DataModel` into typed structs. `rewriteArrays` in `cube/filter.go` writes only the lists that change, from the JSON text of the entries that stay. A property that the tool does not know keeps its bytes.
- A snapshot is the whole data model for Coalesce Quality: a cube or a view that is not in the snapshot is removed there. Say this in the help text of each setting that removes a cube or a view.
- Give each new setting a flag and a `CUBE_*` variable in `cmd/upload.go`, a row in the README and a line in the run summary (`printReport`).

### The variants of a security context

A Cube deployment can compile another data model for each security context (`context_to_app_id`). `Collect` reads the data model with one security context, the default. `CollectVariant` reads the data model of one more security context, with a client whose token has that security context. The code is in `cube/variant.go`.

- `cubemeta.Differing` compares each cube and each view of the variant with the snapshot. The two sides are `cubemeta.Merged`: the response of `/v1/meta` with what only the model export has. Coalesce Quality uses the same function, so the collector and Coalesce Quality agree on what differs. Do not compare in another package.
- The definition is the whole entry of the `cubes` list. Two entries are equal if their JSON values are equal. The order of the properties does not count.
- `CollectVariant` requests `/v1/sql` only for the members of the cubes and the views that differ, in the batches of `Collect`.
- The variant reads the model export only if the snapshot has one. If the snapshot has one and the variant does not get one, `CollectVariant` fails (`ErrVariantModelExport`). Without the model export, each private cube of the variant would look equal, and Coalesce Quality would remove its entity.
- The filter of the options leaves the same parts out of the variant as out of the snapshot.
- The label goes into the path of an entity in Coalesce Quality. `ValidateVariantLabel` allows letters, digits, `_` and `-`, at most 50 characters. Do not allow `@`, `/` or a space.

### The data sources

`Snapshot.DataSources` says what each data source of Cube is, so that Coalesce Quality can find its warehouse. The order of precedence for one name is: `Options.DataSources` (the `--data-source` setting), Cube Cloud (`GET /v1/data-sources`), `Options.EnvironmentDataSources` (the variables of Cube).

- `cube.DataSourcesFromEnv` reads only the variables in `envSuffixes` in `cube/datasource.go`. None of them is a credential. Do not add a variable that holds a user, a password, a key, a token, a secret or a credentials file. `TestFromEnvReadsNoCredential` feeds such variables and asserts that no value is in the result.
- `cube.DataSource` has no field for a credential. Do not add one.
- `Collect` reads no environment variable. The CLI calls `DataSourcesFromEnv(os.Environ())` and gives the result in the options.
- `Collect` keeps `default` and the data sources that the members use. `Filter.Apply` keeps the list as it is.
- `Collect` removes the scheme and the port from `DataSource.Host` (`hostOnly`), and it keeps each name one time.
- A failed read of Cube Cloud is a warning, never an error.

### The git repository

`Snapshot.Git` and `Snapshot.ModelPath` name the git repository of the data model. `Collect` copies them from `Options.Git` and `Options.ModelPath`. It reads no environment variable and runs no program. A nil `ModelPath` means that the directory is not known. An empty `ModelPath` means the root of the repository.

- `cmd/upload.go` (`newGitSettings`) reads the settings. `internal/gitcontext` reads the variables of a CI system and the git checkout.
- Send each URL through `gitcontext.SanitizeURL`. It removes the user name and the password, and it returns an empty text for a text that is not the URL of a remote repository. Do not print a URL that it refused: the text can hold a credential.
- A failure of the `git` binary is silent and gives no git context. Do not add a git library.
- A test of `cmd/` sets `Deps.Environ` and `Deps.WorkDir`, so the test does not read the checkout and the variables of the machine.
- With `--input` the command reads only the settings, and they replace the values of the file.

### The snapshot file

`--output` writes the protojson form of `IngestSnapshotRequest`, and `--input` reads it. This is the one file format of the tool.

- Read the file with `DiscardUnknown: false`, so a typing error in a file that a person edited is an error.
- `quality.NewSnapshot` maps the message of a file to a `cube.Snapshot`. The filter and the counts then work on the snapshot, as after a collection.
- The tool sends its own `uploader_version` and `uploader_build_time`, never the values of the file.
- With `--input` the tool sends no request to Cube. It refuses the flags that set how it reads Cube (`collectionFlags`). Add a new collection flag to that list.
- A file with a field that the message does not have, for example `meta` or `memberSql`, is refused.

## What the tool must never do

- Do not read a credential of a data source of Cube (`CUBEJS_DB_USER`, `CUBEJS_DB_PASS` and the others). See § The data sources.
- Do not call `/v1/load`. The promise to the customer is that no data rows leave the network. `TestSnapshotOfTheRecording` in `cube/` asserts the list of request paths.
- Do not send or print a user name or a password of a git URL.
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
- `buf.build/go/protovalidate` is a dependency of the tests of `internal/quality` only. Do not import it in the code of the binary.
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
