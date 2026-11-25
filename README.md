# synq-cube

`synq-cube` sends the data model of a [Cube](https://cube.dev) deployment to Coalesce Quality.

Run it beside a Cube deployment that Coalesce Quality cannot reach, for example a deployment in a private network. If Coalesce Quality can reach the Cube REST API, you do not need this tool: set the API URL in the Cube integration, and Coalesce Quality reads the data model on a schedule. The two ways send the same snapshot.

From a snapshot, Coalesce Quality shows each cube and each view with its measures, dimensions and segments, the joins, and the lineage to the warehouse tables.

The tool is open source (Apache 2.0), so you can check what it reads and what it sends. See [Build it yourself, change it](#build-it-yourself-change-it).

## Quick start

This is the shortest path from nothing to a data model in Coalesce Quality. Each step has its own section with the details.

1. In Coalesce Quality, create a Cube integration and copy its token. See [Before you start](#before-you-start).
2. Install the tool. See [Install](#install).
3. Set the credentials as environment variables:

   ```bash
   export CUBE_API_URL=https://cube.example.com/cubejs-api
   export CUBE_API_SECRET=...        # the value of CUBEJS_API_SECRET of the deployment
   export QUALITY_TOKEN=...          # the token of the Cube integration
   export QUALITY_REGION=eu          # eu, us or au
   ```

   For Cube Cloud, see [Cube Cloud](#cube-cloud).
4. Collect a snapshot and send nothing:

   ```bash
   synq-cube upload --dry-run --output snapshot.json
   ```

5. Read the summary that the tool prints. Check the count of cubes and views, `Members with an error: 0` and one `Data source` line for each data source. See [Check the result](#check-the-result).
6. Send the snapshot:

   ```bash
   synq-cube upload --input snapshot.json
   ```

7. Open the Cube integration in Coalesce Quality and check that the cubes and the views are there. See [Check the result](#check-the-result).
8. Run `synq-cube upload` after each deploy of the data model, or on a schedule. See [Run it on a schedule](#run-it-on-a-schedule).

If an AI agent runs these steps for you, give it [Run it with an AI agent](#run-it-with-an-ai-agent).

## What it reads

`synq-cube upload` sends two kinds of request to the Cube REST API. They are the same on Cube Core and on Cube Cloud.

| Request | Content |
|---|---|
| `GET /v1/meta?extended` | The compiled data model: the cubes and the views with their measures, dimensions, segments, joins, hierarchies, folders and pre-aggregations |
| `POST /v1/sql` | The SQL that Cube generates for many members. The tool sends one request for the measures and one request for the dimensions of each cube and each view. |
| `GET /v1/sql` | The SQL that Cube generates for one measure or one dimension |

See [How the tool reads the SQL](#how-the-tool-reads-the-sql).

With Cube Cloud credentials, the tool also sends `POST /v1/model-export` and `GET /v1/data-sources` to the Cube Cloud Metadata API. If one of these requests fails, the tool prints a warning and sends the snapshot without its response.

The tool also reads five kinds of variable from its own environment that say what a data source of Cube is, for example `CUBEJS_DB_TYPE`. See [How Coalesce Quality finds the warehouse of a data source](#how-coalesce-quality-finds-the-warehouse-of-a-data-source).

The tool reads the URL, the branch and the commit of the git repository of the data model. See [The git repository of the data model](#the-git-repository-of-the-data-model).

- Cube compiles the SQL for `/v1/sql` and does not run it. No query reaches the warehouse.
- The tool never requests `/v1/load`. No data rows leave your network.
- The tool does not send the security context, the API secret or the token of Cube.
- The SQL text can contain table names, column names and the literal values that the data model has in its expressions and filters.

To see what leaves your network before you send it, use a dry run:

```bash
synq-cube upload --dry-run --output snapshot.json

# The data model, as Cube returned it
jq -r .dataModel snapshot.json | base64 -d | jq .
# Each SQL statement, with its members
jq '.generatedSql[] | {members: [.members[].member], dataSource, sql}' snapshot.json
# The members for which Cube generated no SQL
jq '.memberErrors' snapshot.json
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
Members with SQL: 254, in 39 SQL statements
Members with an error: 0
Requests to /v1/sql: 39
Git repository: https://github.com/example/analytics, branch main, commit 0123abc (from the variables of GitHub Actions)
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

## Check the result

### In the output of the tool

| Line | Good | If it is not |
|---|---|---|
| `Cubes:` and `Views:` | The counts of your data model | The token does not see the whole data model. See [Read access to all members](#read-access-to-all-members). |
| `Members with an error:` | `0` | Read the `warning:` lines. Each has the name of a member and the error text of Cube. Coalesce Quality shows such a member with no lineage. |
| `Data source <name>:` | One line for each data source, with a type and a database, a host or a project | Add `--data-source`. See [How Coalesce Quality finds the warehouse of a data source](#how-coalesce-quality-finds-the-warehouse-of-a-data-source). |
| `Data sources that the snapshot does not describe:` | Absent | The cubes of these data sources have no lineage to the warehouse tables. Add `--data-source` for each. |
| `Git repository:` | The repository, the branch and the commit of the data model | Set `--git-url`, or name the repository in the settings of the Cube integration. |
| `Sent the snapshot to Coalesce Quality` | Present, and the exit status is 0 | See [Troubleshooting](#troubleshooting). |

The tool prints the summary on the standard output, and the warnings and the errors on the standard error.

### In Coalesce Quality

Coalesce Quality reads a snapshot in the background. Allow some minutes for a large data model.

1. Open the settings page of the Cube integration. It shows the time and the result of the last snapshot, with the count of cubes and views and with the warnings.
2. Open the catalog and filter by the Cube integration. Each cube and each view of the snapshot is there, in the folders of its file in the data model.
3. Open one cube. Check its measures and dimensions, and its lineage to the warehouse tables.
4. If a cube has no lineage to a warehouse table, check the `Data source` lines of the run. Coalesce Quality needs a warehouse integration for the database of that data source.

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
| `CUBE_BATCH_SIZE` | `--batch-size` | The largest count of members in one `/v1/sql` request. The default is 200. With 1, the tool sends one request for each member. |
| | `--request-timeout` | The time limit of one request to Cube. The default is `1m`. |

Set one credential. If more than one is set, the tool uses the first of them in this order: `CUBE_CLOUD_API_KEY`, `CUBE_API_TOKEN`, `CUBE_API_SECRET`.

### Data sources

See [How Coalesce Quality finds the warehouse of a data source](#how-coalesce-quality-finds-the-warehouse-of-a-data-source).

| Variable | Flag | Meaning |
|---|---|---|
| `CUBE_DATA_SOURCES` | `--data-source <text>` | What a data source is, as `name=type[,database=..][,host=..][,project=..]`. Set the flag more than once, or separate the entries in the variable with semicolons. |
| `CUBE_NO_DATA_SOURCES_FROM_ENV` | `--no-data-sources-from-env` | Do not read the `CUBEJS_DB_*` and `CUBEJS_DS_*` variables of Cube. The variable takes `true` or `false`. |

### Git repository

See [The git repository of the data model](#the-git-repository-of-the-data-model).

| Variable | Flag | Meaning |
|---|---|---|
| `CUBE_GIT_URL` | `--git-url` | The URL of the git repository that holds the files of the data model, for example `https://github.com/example/analytics` |
| `CUBE_GIT_BRANCH` | `--git-branch` | The branch of the data model |
| `CUBE_GIT_COMMIT` | `--git-commit` | The commit of the data model |
| `CUBE_MODEL_PATH` | `--model-path` | The path of the directory of the data model in the repository. The default is `model`, if the git checkout has that directory. |
| `CUBE_NO_GIT_CONTEXT` | `--no-git-context` | Do not read the git repository from the variables of a CI system and from the git checkout. The variable takes `true` or `false`. |

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

## Run it with an AI agent

This section is for a coding agent, for example Claude Code, that sets the tool up for a person. All of it is true for a person too.

The tool is safe to drive from an agent:

- It never asks a question. Each setting is a flag or an environment variable, and `synq-cube upload --help` lists them all.
- A run with `--dry-run` sends nothing to Coalesce Quality and needs no `QUALITY_TOKEN`.
- No run changes the Cube deployment, and no run starts a query on the warehouse.
- The exit status is 0 only if the run did all that it had to do. See [Exit status](#exit-status).

What the agent needs from the person:

- `QUALITY_TOKEN` and the region. Only a person can create the Cube integration, in Coalesce Quality.
- One credential of Cube: `CUBE_API_SECRET`, `CUBE_API_TOKEN` or `CUBE_CLOUD_API_KEY`.
- Network access from the machine of the agent to the Cube REST API.

Rules for the agent:

1. Read each secret from the environment. Do not write a secret in a command line, in a file of the repository or in your answer.
2. Do not print the snapshot file. It can hold the SQL of a private data model. Read it with the `jq` commands below.
3. Start with a dry run. Send the snapshot only after the checks pass, or after the person agrees to send it with the gaps that you found.
4. Send the same file that you checked, with `--input`. Do not collect a second time.
5. Use the same filters on each run. A cube or a view that is not in a snapshot is removed from Coalesce Quality.

The sequence:

```bash
# 1. Is the tool there?
synq-cube --version

# 2. Are the settings there? Print only the names, never the values.
env | grep -E '^(CUBE_|QUALITY_)' | cut -d= -f1

# 3. Collect and send nothing. Keep the two streams.
synq-cube upload --dry-run --output snapshot.json > run.out 2> run.err; echo "exit status $?"
cat run.out
grep -c '^warning:' run.err

# 4. Check the snapshot.
jq -r .dataModel snapshot.json | base64 -d | jq -r '.cubes[] | "\(.type) \(.name)"'
jq '[.generatedSql[].members[]] | length' snapshot.json
jq '.memberErrors // [] | map({member, error})' snapshot.json
jq '.dataSources' snapshot.json
jq '{gitContext, modelPath}' snapshot.json

# 5. Send the file that you checked.
synq-cube upload --input snapshot.json; echo "exit status $?"
```

Stop and ask the person if one of these is true:

- The exit status of step 3 is not 0. Give the last lines of `run.err`, and see [Troubleshooting](#troubleshooting).
- The data model has fewer cubes or views than the person expects. The token does not see all of it.
- More than a few members have an error. Each such member has no lineage in Coalesce Quality.
- A data source has no description, or `dataSources` is absent. Ask for the database type and the database, then add `--data-source`.
- `gitContext` names a repository that does not hold the data model. Set `--git-url`, or `--no-git-context`.

After step 5, tell the person what you sent: the counts, the members with an error, the data sources and the git repository. Then give them [In Coalesce Quality](#in-coalesce-quality), as only a person with access to the workspace can do that check.

To make the run permanent, add it to the pipeline that deploys the data model. See [Run it on a schedule](#run-it-on-a-schedule). Keep the secrets in the secret store of the CI system.

## How the tool reads the SQL

Cube generates the SQL of a query of many members in one statement. The statement has one output column for each member. So the tool does not send one request for each member.

1. For each cube and each view, the tool sends one `POST /v1/sql` request for all its measures and one for all its dimensions. The query has `"ungrouped": true`, so Cube reads the tables of the cube and not a pre-aggregation table.
2. A request holds 200 members at most. The tool splits a longer list. Change the limit with `--batch-size`.
3. If Cube returns an error for a request, one member or more breaks the query. The tool splits the members in two halves and sends one request for each half, down to one member. This finds the members that break the query. Each list that Cube answers is one SQL statement of the snapshot.
4. The tool sends the members that it found in step 3 in a query without `ungrouped`, and splits that list in the same way. Some measures have SQL only in a query without `ungrouped`.
5. A member that fails alone in step 4 has no SQL. The snapshot holds it in `memberErrors` with the error text of Cube, and the tool prints one warning.

- One member for which Cube generates no SQL does not stop the SQL of the other members.
- A member gets a query without `ungrouped` only if its ungrouped query failed when it was alone.
- `--concurrency` is the count of requests that run at the same time. Cube generates SQL in one process, so a larger value gives little.
- `--batch-size 1` is the old behaviour: one `GET /v1/sql` request for each measure and each dimension, and one SQL statement for each member. Use it if you must compare the SQL of one member with the SQL in the Cube Playground. It is much slower on a large data model.
- A query of one member is a `GET` request with the query in the URL. A query of many members is a `POST` request with the query in the body, as a URL has a length limit: Cube refuses a URL of more than about 16,000 characters. If your Cube answers `POST /v1/sql` with status 404 or 405, the tool sends the queries of many members in `GET` requests, and splits a list that is too long for the URL.
- The response of Cube says which output column is which member (`aliasNameToMember`). The tool copies the name of the column from the response. It never builds the name from the name of the member, as a cube with `sql_alias` has another prefix. If the response does not name the output column of a member, the tool sends one request for that member alone. If that response does not name the column, the member has no SQL: it is in `memberErrors`, with a text that says so.
- A segment is a filter and has no output column. The tool sends no query for a segment.
- The time of a snapshot (`collectedAt`) is the time at which the tool started, before its first request to Cube.
- The tool prints the count of `/v1/sql` requests that it sent.

## The git repository of the data model

The snapshot can name the git repository that holds the files of the data model: the URL, the branch and the commit, and the path of the directory of the data model. Coalesce Quality then links each cube and each view to its file in the repository.

The tool takes the repository from the first of these places that names one:

1. **The settings**: `--git-url`, `--git-branch`, `--git-commit` (or `CUBE_GIT_URL`, `CUBE_GIT_BRANCH`, `CUBE_GIT_COMMIT`).
2. **GitHub Actions**: the variables `GITHUB_SERVER_URL` and `GITHUB_REPOSITORY`, `GITHUB_HEAD_REF` or `GITHUB_REF_NAME`, and `GITHUB_SHA`.
3. **GitLab CI**: the variables `CI_PROJECT_URL`, `CI_COMMIT_REF_NAME` and `CI_COMMIT_SHA`.
4. **The git checkout** of the working directory. The tool runs the `git` binary and reads the URL of the remote `origin`, the branch and the commit. If there is no `git` binary or no checkout, this place gives nothing, and the tool prints no error.

- The tool never sends a user name or a password of a URL. It removes them: `https://user:token@github.com/example/analytics` becomes `https://github.com/example/analytics`. The short SSH form `git@github.com:example/analytics.git` stays as it is.
- With `--git-url`, the branch and the commit come only from the settings. Without `--git-url`, `--git-branch` and `--git-commit` replace the value of the place that names the repository.
- `--model-path` is the directory of the data model, from the root of the repository, with `/` between the parts. The file name of a cube in the data model is relative to it. For the root of the repository, set `--model-path .`. If you do not set it and the git checkout has a directory `model`, the tool sends `model`. Else the tool sends no path: the directory is not known.
- The tool prints the repository that it sends, with the place that it came from.
- To send no repository, set `--no-git-context` and no `--git-url`.
- If the snapshot names no repository, you can name the repository in the settings of the Cube integration in Coalesce Quality.
- If you run the tool in a checkout of another repository, for example a repository of deploy scripts, set `--git-url` or `--no-git-context`. If you do not, the tool sends that repository.
- With `--input` the tool sends the repository of the file. It does not read the variables of a CI system and the checkout. A `--git-*` or `--model-path` setting replaces the value of the file.

## How Coalesce Quality finds the warehouse of a data source

Each cube reads one data source of Cube, for example `default`. For the lineage to the warehouse tables, Coalesce Quality must know which of your warehouse integrations a data source is. The snapshot says what each data source is: the name, the database type, and the database, the host or the project. Coalesce Quality compares that with your warehouse integrations.

The tool takes the description of a data source from three places. If two places describe the same data source, the first in this list wins.

1. **`--data-source`.** Use it if the other two places are not available.

   ```bash
   synq-cube upload \
     --data-source 'default=snowflake,database=ANALYTICS,host=ab12345.eu-west-1' \
     --data-source 'events=bigquery,project=example-project'
   ```

   The type is the database type as Cube spells it in `CUBEJS_DB_TYPE` (`snowflake`, `bigquery`, `postgres`, `redshift`, `clickhouse`). For Snowflake, `host` is the account. The snapshot holds the host with no scheme and no port: the tool removes them.

2. **Cube Cloud.** With Cube Cloud credentials this is automatic: the tool reads the data sources from Cube Cloud. Cube Cloud lists only the data sources that connect.

3. **The environment of Cube**, for Cube Core. Run the tool with the same env file as Cube:

   ```bash
   docker run --rm --env-file cube.env \
     -e CUBE_API_URL -e QUALITY_TOKEN -e QUALITY_REGION \
     ghcr.io/getsynq/synq-cube:latest
   ```

   The tool reads these variables, and no other variable of Cube:

   | Variable | Goes to |
   |---|---|
   | `CUBEJS_DB_TYPE` | the type |
   | `CUBEJS_DB_NAME` | the database |
   | `CUBEJS_DB_HOST` | the host |
   | `CUBEJS_DB_SNOWFLAKE_ACCOUNT` | the host of a Snowflake data source |
   | `CUBEJS_DB_BQ_PROJECT_ID` | the project |

   These are the data source `default`. With the prefix `CUBEJS_DS_<NAME>_`, for example `CUBEJS_DS_EVENTS_DB_TYPE`, they are the data source `<name>`. The name has the spelling of `CUBEJS_DATASOURCES`, or lower case if it is not in that list.

- The tool reads no credential. It never reads a user, a password, a key, a token or a credentials file of a data source, and the snapshot has no field for one. A test in this repository feeds such variables and asserts it.
- The snapshot holds `default` and the data sources that the members of the snapshot use.
- The tool prints one line for each data source, with the place that it came from, and one line with the data sources that it could not describe:

  ```text
  Data source default: snowflake, database ANALYTICS, host ab12345.eu-west-1 (from the environment of Cube)
  Data source events: bigquery, project example-project (from --data-source)
  Data sources that the snapshot does not describe: postgres
  ```

- To stop the read of the environment, set `--no-data-sources-from-env`.
- The data source mapping in the settings of the Cube integration is the override. If it names a warehouse integration for a data source, Coalesce Quality uses it and not the description of the snapshot.
- With `--input` the tool sends the data sources of the file, and it does not read the environment.

## Inspect, edit and send a snapshot file

A snapshot file is the protojson form of `synq.ingest.cube.v1.IngestSnapshotRequest`. It holds all that the tool sends. Use it to look at a snapshot, to keep one, or to send one at a later time.

```bash
# 1. Write the snapshot to a file and send nothing
synq-cube upload --dry-run --output snapshot.json

# 2. Look at it
jq -r .dataModel snapshot.json | base64 -d | jq '.cubes[].name'
jq '.generatedSql[] | {members: [.members[].member], dataSource, sql}' snapshot.json
jq '.memberErrors' snapshot.json

# 3. Send it
synq-cube upload --input snapshot.json
```

- With `--input` the tool sends no request to Cube and needs no `CUBE_*` credential. It needs `QUALITY_TOKEN`.
- The tool refuses the flags that set how it reads Cube (`--cube-api-url`, `--concurrency` and the others), as it does not read Cube.
- The tool refuses a file with a field that the message does not have, a file with no `dataModel`, a `dataModel` that is not JSON with a `cubes` list, an entry of `generatedSql` with no `sql`, no member or no `column` of a member, and a file with no `collectedAt`. So a typing error in a file that you edited is an error, not a silent change.
- The file has two lists for the SQL. `generatedSql` holds the SQL statements. Each statement has its `members`, and each member has the `column` of the statement that holds its value. `memberErrors` holds the members with no SQL, each with the reason. A member is in one of the two lists, one time.
- The file never has an optional field with an empty text.
- The tool prints the same counts as after a collection, counted from the file.
- The tool sends its own `uploaderVersion` and `uploaderBuildTime`, not the values of the file.
- `--input` with `--dry-run` only checks the file and prints the counts.
- `--input` with `--output` writes the file again in the format of the tool.
- `dataModel` and `modelExport` are JSON texts in base64, as protojson writes bytes. To change one, decode it, change it and encode it again.

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
| `--skip-member-sql` | The SQL of each member, and the members with an error | No lineage to the warehouse tables, and no data source for each cube |
| `--skip-model-export` | The Cube Cloud model export | The snapshot has only what the REST API returns. |

- A pattern is a glob on the name of a cube or a view: `*` is each sequence of characters, `?` is one character, `[abc]` is one character of a set. The match is on the whole name and is case sensitive, as the names in Cube are. `orders*` matches `orders` and `orders_overview`, and it does not match `pg_orders`.
- A name stays if it matches an `--include` pattern, or if there is no `--include` pattern, and if it matches no `--exclude` pattern.
- A pattern that is not valid stops the tool before it sends a request.
- The cube or the view is removed from the `dataModel` of the snapshot. The other properties of `dataModel` keep their text, also the properties that the tool does not know.
- On a file, a filter removes the members that it leaves out from each SQL statement, and it removes a statement with no member left. The SQL text of a statement does not change: it still has the output column of a member that was removed. On a collection, the tool asks Cube only for the members that stay.
- A view that includes members of a cube that is left out stays in the snapshot. It has no lineage through that cube. The tool prints one warning for each such view and cube. Many projects keep the cubes hidden and show only the views, so with `--skip-hidden` the views of such a project have no lineage through their cubes.
- The model export cannot be filtered. If a filter removes a cube, a view or a member, the snapshot has no model export, and the tool says so.
- If the filters leave no cube and no view, the tool stops with an error and sends nothing.
- The tool prints how many cubes, views and members each setting left out:

```text
Cubes: 9
Views: 5
Members: 218
Members with SQL: 218, in 27 SQL statements
Members with an error: 0
Requests to /v1/sql: 27
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

The API key needs only two permissions on one deployment. An admin of the Cube Cloud account creates the key with the Cube Cloud API and an API key of their own:

```bash
curl -s -X POST "$CUBE_CLOUD_URL/api/v1/api-keys" \
  -H "Authorization: Bearer $CUBE_CLOUD_ADMIN_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "coalesce-quality",
    "deploymentIds": [42],
    "allowedEndpoints": [
      "DeploymentEnvironmentPublicController.getDeploymentEnvironments",
      "DeploymentEnvironmentPublicController.createDeploymentEnvironmentTokenForMetaSync"
    ]
  }'
```

- `deploymentIds` limits the key to the deployment. `allowedEndpoints` limits it to the list of the environments and to the token for the metadata of the deployment.
- The response holds the key one time, in `token`. Keep it in a secret store.
- A key with these two permissions gives the full snapshot: the data model, the SQL of the members, the model export and the data sources.
- To give the key an end date, add `"ttlHours"`.

The token that Cube Cloud creates has the scopes `meta` and `meta-private`. If the deployment refuses the `/v1/sql` requests of that token, the tool prints a warning for each member.

You can also use `CUBE_API_URL` with `CUBE_API_SECRET` or `CUBE_API_TOKEN` on Cube Cloud, as on Cube Core. The snapshot then has no model export.

## Troubleshooting

| Message | Cause |
|---|---|
| `cannot read the data model: ... HTTP 403` | Cube refused the token. Check the API secret, the security context and the `meta` scope. |
| `cannot read the data model: ... HTTP 404` | `CUBE_API_URL` does not have the base path. The default base path of Cube is `/cubejs-api`. |
| `warning: <member>: Cube generated no SQL: ...` | Cube returned an error for the member. The text after the colon is the error of Cube. Run the same member in the Cube Playground to see it. |
| `warning: <member>: ... has no entry for the member in aliasNameToMember ...` | The response of Cube did not say which output column is the member. This version of Cube is too old for the tool, or a proxy changed the response. |
| `cannot get an access token for Coalesce Quality` | `QUALITY_TOKEN` is not the token of a Cube integration, or the region is not the region of the workspace. |
