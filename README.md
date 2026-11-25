# synq-cube

`synq-cube` sends the data model of a [Cube](https://cube.dev) deployment to Coalesce Quality.

Run it beside a Cube deployment that Coalesce Quality cannot reach, for example a deployment in a private network. If Coalesce Quality can reach the Cube REST API, you do not need this tool: set the API URL in the Cube integration, and Coalesce Quality reads the data model on a schedule. The two ways send the same snapshot.

From a snapshot, Coalesce Quality shows each cube and each view with its measures, dimensions and segments, the joins, and the lineage to the warehouse tables.

The tool is open source (Apache 2.0), so you can check what it reads and what it sends. See [Build it yourself, change it](#build-it-yourself-change-it).

## What it reads

`synq-cube upload` sends two kinds of request to the Cube REST API. They are the same on Cube Core and on Cube Cloud.

| Request | Content |
|---|---|
| `GET /v1/meta?extended` | The compiled data model: the cubes and the views with their measures, dimensions, segments, joins, hierarchies, folders and pre-aggregations |
| `GET /v1/sql` | The SQL that Cube generates for one measure or one dimension. The tool sends one request for each measure and each dimension. |

With Cube Cloud credentials, the tool also sends `POST /v1/model-export` to the Cube Cloud Metadata API. If that request fails, the tool prints a warning and sends the snapshot without it.

- Cube compiles the SQL for `/v1/sql` and does not run it. No query reaches the warehouse.
- The tool never requests `/v1/load`. No data rows leave your network.
- The tool does not send the security context, the API secret or the token of Cube.
- The SQL text can contain table names, column names and the literal values that the data model has in its expressions and filters.

To see what leaves your network before you send it, use a dry run:

```bash
synq-cube upload --dry-run --output snapshot.json

# The data model, as Cube returned it
jq -r .meta snapshot.json | base64 -d | jq .
# The SQL of each member
jq '.memberSql[] | {member, dataSource, sql, error}' snapshot.json
```

A dry run sends nothing to Coalesce Quality and needs no Coalesce Quality token. The file is the protojson form of `synq.ingest.cube.v1.IngestSnapshotRequest`, a message of the [public API](https://buf.build/getsynq/api). See [Inspect, edit and send a snapshot file](#inspect-edit-and-send-a-snapshot-file).

## Before you start

1. In Coalesce Quality, create a Cube integration. Give it the name of the deployment and leave the API URL empty.
2. Copy the token from the settings page of the integration. This is `QUALITY_TOKEN`.
3. Prepare a credential for the Cube REST API (see [The Cube token](#the-cube-token)).

The integration names the deployment, so the tool has no setting for it. One integration is one compiled data model.

## Install

### Docker

```bash
docker pull ghcr.io/getsynq/synq-cube:latest
```

The image runs `synq-cube upload` by default. To pin a version, use a tag from the [releases page](https://github.com/getsynq/synq-cube/releases), for example `:v0.1.0`.

### Binary

Each release has archives for Linux, macOS and Windows, on amd64 and arm64, and a `checksums.txt`.

```bash
VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
  https://github.com/getsynq/synq-cube/releases/latest | sed 's#.*/v##')
OS=$(uname -s | tr '[:upper:]' '[:lower:]')          # darwin or linux
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')

curl -fL "https://github.com/getsynq/synq-cube/releases/download/v${VERSION}/synq-cube_${VERSION}_${OS}_${ARCH}.tar.gz" \
  | tar -xz
sudo mv synq-cube /usr/local/bin/
synq-cube --version
```

### Go

```bash
go install github.com/getsynq/synq-cube@latest
```

## Build it yourself, change it

The full source is in this repository, so you can read what the tool does and change it.

```bash
git clone https://github.com/getsynq/synq-cube.git
cd synq-cube
go build -o synq-cube .
go test ./...
```

- `cube/` reads the Cube REST API and builds the snapshot. It is the only code that sends requests to Cube and to Cube Cloud.
- `internal/quality/` sends the snapshot to Coalesce Quality. It is the only code that sends requests to Coalesce Quality.
- `cmd/` holds the settings and the output.

To see exactly what your build sends, write the snapshot to a file and send nothing:

```bash
./synq-cube upload --dry-run --output snapshot.json
```

The tests run against a recorded Cube deployment and need no network access.

## Run it

```bash
export CUBE_API_URL=https://cube.example.com/cubejs-api
export CUBE_API_SECRET=...        # the value of CUBEJS_API_SECRET of the deployment
export QUALITY_TOKEN=...          # the token of the Cube integration
export QUALITY_REGION=eu          # eu, us or au

synq-cube upload
```

```text
Reading the data model from https://cube.example.com/cubejs-api
Cubes: 14
Views: 6
Members: 254
Members with an error: 0
Sent the snapshot to Coalesce Quality, eu (developer.synq.io:443)
```

The same with Docker:

```bash
docker run --rm \
  -e CUBE_API_URL -e CUBE_API_SECRET -e QUALITY_TOKEN -e QUALITY_REGION \
  ghcr.io/getsynq/synq-cube:latest
```

### Exit status

| Result | Exit status |
|---|---|
| The snapshot was sent | 0 |
| Cube generated no SQL for some members. The tool prints one warning for each, and sends the snapshot with the error text of Cube. | 0 |
| The `/v1/meta` request failed, or the response was not a data model | 1 |
| The upload to Coalesce Quality failed | 1 |

The tool sends a request to Cube again after status 5xx, status 429, a network error and the `Continue wait` response. It sends the upload again if the Coalesce Quality API is not available.

## Settings

Each setting is an environment variable. The settings that are not secrets have a flag too, and the flag wins.

### Cube

| Variable | Flag | Meaning |
|---|---|---|
| `CUBE_API_URL` | `--cube-api-url` | The base URL of the Cube REST API with the base path, for example `https://cube.example.com/cubejs-api`. With Cube Cloud credentials it is optional: the tool reads the URL from Cube Cloud. |
| `CUBE_API_SECRET` | | The API secret of the deployment (`CUBEJS_API_SECRET`). The tool signs a token with it that is valid for 15 minutes. |
| `CUBE_API_TOKEN` | | A JSON Web Token that you made. Use it for a deployment that checks tokens with JWKS. The tool sends it as the `Authorization` header with no change. |
| `CUBE_SECURITY_CONTEXT` | `--cube-security-context` | The security context for the token that the tool makes, as a JSON object. The default is an empty security context. Not used with `CUBE_API_TOKEN`. |
| `CUBE_SECURITY_CONTEXT_LABEL` | `--security-context-label` | A name for the security context, for example `full-read`. The tool sends the name, never the security context. |
| `CUBE_VERSION` | `--cube-version` | The version of Cube, for example `1.3.30`. Optional. The Cube REST API does not report it. |
| `CUBE_CLOUD_URL` | `--cube-cloud-url` | The URL of the Cube Cloud account, for example `https://example.cubecloud.dev` |
| `CUBE_CLOUD_API_KEY` | | An API key of Cube Cloud. The tool creates a token for the deployment with it. |
| `CUBE_CLOUD_DEPLOYMENT_ID` | `--cube-cloud-deployment-id` | The id of the deployment, the number in the URL of the deployment in Cube Cloud |
| `CUBE_CLOUD_ENVIRONMENT_ID` | `--cube-cloud-environment-id` | The id of the environment. The default is the production environment. |
| | `--concurrency` | How many `/v1/sql` requests run at the same time. The default is 4. |
| | `--request-timeout` | The time limit of one request to Cube. The default is `1m`. |

Set one credential. If more than one is set, the tool uses the first of them in this order: `CUBE_CLOUD_API_KEY`, `CUBE_API_TOKEN`, `CUBE_API_SECRET`.

### Filters

See [Leave parts of the data model out](#leave-parts-of-the-data-model-out) before you use them.

| Variable | Flag | Meaning |
|---|---|---|
| `CUBE_INCLUDE` | `--include <pattern>` | Keep only the cubes and the views with a name that matches the pattern. Set the flag more than once, or separate the patterns in the variable with commas. |
| `CUBE_EXCLUDE` | `--exclude <pattern>` | Leave out the cubes and the views with a name that matches the pattern. It wins over `--include`. |
| `CUBE_SKIP_HIDDEN` | `--skip-hidden` | Leave out the cubes, the views and the members that are not public |
| `CUBE_SKIP_MEMBER_SQL` | `--skip-member-sql` | Send no `/v1/sql` request. The snapshot has no SQL of the members. |
| `CUBE_SKIP_MODEL_EXPORT` | `--skip-model-export` | Send no model export request to Cube Cloud |

The three `CUBE_SKIP_*` variables take `true` or `false`.

### Coalesce Quality

| Variable | Flag | Meaning |
|---|---|---|
| `QUALITY_TOKEN` | | The token of the Cube integration |
| `QUALITY_REGION` | `--region` | The region of your Coalesce Quality workspace: `eu`, `us` or `au`. The default is `eu`. |
| `QUALITY_API_ENDPOINT` | `--endpoint` | The API endpoint as `host:port`. Use it only if Coalesce Quality gave you one. It wins over the region. |

### Output

| Flag | Meaning |
|---|---|
| `--output <file>` | Write the snapshot to the file as JSON |
| `--input <file>` | Send the snapshot of the file and send no request to Cube. `-` is the standard input. |
| `--dry-run` | Send nothing to Coalesce Quality. Use it with `--output`, or with `--input` to check a file. |

## Inspect, edit and send a snapshot file

A snapshot file is the protojson form of `synq.ingest.cube.v1.IngestSnapshotRequest`. It holds all that the tool sends. Use it to look at a snapshot, to keep one, or to send one at a later time.

```bash
# 1. Write the snapshot to a file and send nothing
synq-cube upload --dry-run --output snapshot.json

# 2. Look at it
jq -r .meta snapshot.json | base64 -d | jq '.cubes[].name'
jq '.memberSql[] | {member, dataSource, sql, error}' snapshot.json

# 3. Send it
synq-cube upload --input snapshot.json
```

- With `--input` the tool sends no request to Cube and needs no `CUBE_*` credential. It needs `QUALITY_TOKEN`.
- The tool refuses the flags that set how it reads Cube (`--cube-api-url`, `--concurrency` and the others), as it does not read Cube.
- The tool refuses a file with a field that the message does not have, a file with no `meta`, a `meta` that is not JSON with a `cubes` list, and a file with no `collectedAt`. So a typing error in a file that you edited is an error, not a silent change.
- The tool prints the same counts as after a collection, counted from the file.
- The tool sends its own `uploaderVersion` and `uploaderBuildTime`, not the values of the file.
- `--input` with `--dry-run` only checks the file and prints the counts.
- `--input` with `--output` writes the file again in the format of the tool.
- `meta` and `modelExport` are JSON texts in base64, as protojson writes bytes. To change one, decode it, change it and encode it again.

The file can hold the SQL of your data model, with table names, column names and literal values. Treat it like source code.

## Leave parts of the data model out

Five settings leave parts of the data model out of the snapshot. They work on a collection and on a snapshot file, and the result is the same.

```bash
# On a collection: the tool sends no /v1/sql request for a member that is left out
synq-cube upload --exclude 'tmp_*' --exclude 'scratch_*' --skip-hidden

# On a file: cut it down, look at the result, then send it
synq-cube upload --input snapshot.json --exclude 'tmp_*' --dry-run --output smaller.json
synq-cube upload --input smaller.json
```

**A snapshot is the whole data model for Coalesce Quality. A cube or a view that is not in the snapshot is removed from Coalesce Quality.** A filter is not a way to update a part of the data model. Use the same filter on each run.

| Setting | What is left out | The price |
|---|---|---|
| `--include`, `--exclude` | The cubes and the views that the patterns do not keep, with their members | Coalesce Quality does not show them. |
| `--skip-hidden` | The cubes and the views with `public: false` (or the older `isVisible: false`), and the members of the other cubes and views that are not public | Coalesce Quality does not show them. |
| `--skip-member-sql` | The SQL of each member | No lineage to the warehouse tables, and no data source for each cube |
| `--skip-model-export` | The Cube Cloud model export | The snapshot has only what the REST API returns. |

- A pattern is a glob on the name of a cube or a view: `*` is each sequence of characters, `?` is one character, `[abc]` is one character of a set. The match is on the whole name and is case sensitive, as the names in Cube are. `orders*` matches `orders` and `orders_overview`, and it does not match `pg_orders`.
- A name stays if it matches an `--include` pattern, or if there is no `--include` pattern, and if it matches no `--exclude` pattern.
- A pattern that is not valid stops the tool before it sends a request.
- The cube or the view is removed from the `meta` of the snapshot. The other properties of `meta` keep their text, also the properties that the tool does not know.
- A view that includes members of a cube that is left out stays in the snapshot. It has no lineage through that cube. The tool prints one warning for each such view and cube. Many projects keep the cubes hidden and show only the views, so with `--skip-hidden` the views of such a project have no lineage through their cubes.
- The model export cannot be filtered. If a filter removes a cube, a view or a member, the snapshot has no model export, and the tool says so.
- If the filters leave no cube and no view, the tool stops with an error and sends nothing.
- The tool prints how many cubes, views and members each setting left out:

```text
Cubes: 9
Views: 5
Members: 218
Members with an error: 0
Left out by --include and --exclude: cubes 5, views 1, members 36
```

## The Cube token

The data model that Coalesce Quality shows is the data model that the token can see. Check three things.

### API scopes

The token needs the API scopes `meta` (for `/v1/meta`) and `sql` (for `/v1/sql`). Cube gives both by default. If your deployment sets `context_to_api_scopes`, give the security context of this tool the two scopes:

```python
from cube import config

@config('context_to_api_scopes')
def context_to_api_scopes(security_context, default_scopes):
    if security_context.get('service') == 'coalesce-quality':
        return ['meta', 'sql']
    return default_scopes
```

```bash
export CUBE_SECURITY_CONTEXT='{"service": "coalesce-quality"}'
```

The tool does not need the `data` scope.

If the token has no `sql` scope, each member has an error and the lineage to the warehouse tables is absent.

### Read access to all members

Access policies and `query_rewrite` apply to the security context of the token. A cube, a view or a member that is hidden from the security context is not in the snapshot, and Coalesce Quality then removes it. Give the security context of this tool read access to all cubes, views and members.

### One integration for each tenant

A deployment with `context_to_app_id` compiles one data model for each tenant. One Cube integration in Coalesce Quality is one data model. For each tenant:

1. Create one Cube integration in Coalesce Quality.
2. Run the tool with the token of that integration and with a security context of that tenant.
3. Set `--security-context-label` to a name of the tenant, so the snapshot says which data model it is.

## Run it on a schedule

A snapshot holds the whole data model. A cube or a view that is not in the newest snapshot is removed from Coalesce Quality. Send a snapshot after each change of the data model, or on a schedule.

### Kubernetes CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: synq-cube
spec:
  schedule: "15 * * * *"
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      backoffLimit: 1
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: synq-cube
              image: ghcr.io/getsynq/synq-cube:latest
              args: ["upload", "--security-context-label", "full-read"]
              env:
                - name: CUBE_API_URL
                  value: http://cube-api.cube.svc.cluster.local:4000/cubejs-api
                - name: QUALITY_REGION
                  value: eu
                - name: CUBE_API_SECRET
                  valueFrom:
                    secretKeyRef: {name: synq-cube, key: cube-api-secret}
                - name: QUALITY_TOKEN
                  valueFrom:
                    secretKeyRef: {name: synq-cube, key: quality-token}
```

The pod needs network access to the Cube API and outbound HTTPS access to the Coalesce Quality API of your region.

### CI job after a deploy of the data model

Run the tool as the last step of the pipeline that deploys the data model, from a runner that can reach the Cube API. Example for GitHub Actions:

```yaml
- name: Send the Cube data model to Coalesce Quality
  run: |
    docker run --rm \
      -e CUBE_API_URL -e CUBE_API_SECRET -e QUALITY_TOKEN -e QUALITY_REGION \
      ghcr.io/getsynq/synq-cube:latest
  env:
    CUBE_API_URL: https://cube.internal.example.com/cubejs-api
    CUBE_API_SECRET: ${{ secrets.CUBE_API_SECRET }}
    QUALITY_TOKEN: ${{ secrets.QUALITY_TOKEN }}
    QUALITY_REGION: eu
```

Wait until Cube serves the new data model before the step runs.

## Cube Cloud

For a deployment on Cube Cloud, the tool can create its own token from an API key of Cube Cloud:

```bash
export CUBE_CLOUD_URL=https://example.cubecloud.dev
export CUBE_CLOUD_API_KEY=...
export CUBE_CLOUD_DEPLOYMENT_ID=42
export QUALITY_TOKEN=...

synq-cube upload
```

The token that Cube Cloud creates has the scopes `meta` and `meta-private`. If the deployment refuses the `/v1/sql` requests of that token, the tool prints a warning for each member.

You can also use `CUBE_API_URL` with `CUBE_API_SECRET` or `CUBE_API_TOKEN` on Cube Cloud, as on Cube Core. The snapshot then has no model export.

## Troubleshooting

| Message | Cause |
|---|---|
| `cannot read the data model: ... HTTP 403` | Cube refused the token. Check the API secret, the security context and the `meta` scope. |
| `cannot read the data model: ... HTTP 404` | `CUBE_API_URL` does not have the base path. The default base path of Cube is `/cubejs-api`. |
| `warning: <member>: Cube generated no SQL: ...` | Cube returned an error for the member. The text after the colon is the error of Cube. Run the same member in the Cube Playground to see it. |
| `cannot get an access token for Coalesce Quality` | `QUALITY_TOKEN` is not the token of a Cube integration, or the region is not the region of the workspace. |
