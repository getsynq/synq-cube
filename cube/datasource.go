package cube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
)

// DefaultDataSource is the name of the data source of a cube that sets no `data_source`.
const DefaultDataSource = "default"

// DataSource says what one data source of a Cube deployment is, so that Coalesce Quality can
// find the warehouse of the data source. It holds no credential. An empty field means that the
// value is not known.
type DataSource struct {
	// Name is the name of the data source in Cube (`default`).
	Name string
	// Type is the database type, as Cube spells it (`snowflake`, `bigquery`, `postgres`).
	Type string
	// Database is the database of the data source.
	Database string
	// Host is the host of the database, with no scheme and no port. For Snowflake it is the
	// account. Collect removes a scheme and a port.
	Host string
	// Project is the BigQuery project.
	Project string
}

// DataSourceOrigin says where the description of a data source came from.
type DataSourceOrigin string

const (
	// OriginCubeCloud is a data source that the Metadata API of Cube Cloud returned.
	OriginCubeCloud DataSourceOrigin = "Cube Cloud"
	// OriginEnvironment is a data source from Options.EnvironmentDataSources.
	OriginEnvironment DataSourceOrigin = "the environment of Cube"
	// OriginSetting is a data source from Options.DataSources.
	OriginSetting DataSourceOrigin = "a setting"
)

// The variables of Cube that DataSourcesFromEnv reads, without the `CUBEJS_` or the
// `CUBEJS_DS_<NAME>_` prefix. None of them is a credential. Do not add a variable that holds a
// user, a password, a key, a token or a secret.
const (
	envSuffixType             = "DB_TYPE"
	envSuffixName             = "DB_NAME"
	envSuffixHost             = "DB_HOST"
	envSuffixSnowflakeAccount = "DB_SNOWFLAKE_ACCOUNT"
	envSuffixBigQueryProject  = "DB_BQ_PROJECT_ID"
)

var envSuffixes = []string{envSuffixType, envSuffixName, envSuffixHost, envSuffixSnowflakeAccount, envSuffixBigQueryProject}

const (
	envPrefix            = "CUBEJS_"
	envDataSourcePrefix  = "CUBEJS_DS_"
	envDataSourcesList   = "CUBEJS_DATASOURCES"
	snowflakeType        = "snowflake"
	dataSourcesPath      = "/v1/data-sources"
	dataSourceKeyDB      = "database"
	dataSourceKeyHost    = "host"
	dataSourceKeyProject = "project"
)

// DataSourcesFromEnv reads the data sources from the environment of Cube. environ has the form
// of os.Environ: one `NAME=value` text for each variable.
//
// It reads `CUBEJS_DB_TYPE`, `CUBEJS_DB_NAME`, `CUBEJS_DB_HOST`, `CUBEJS_DB_SNOWFLAKE_ACCOUNT`
// and `CUBEJS_DB_BQ_PROJECT_ID` as the data source `default`. It reads the same variables with
// the prefix `CUBEJS_DS_<NAME>_` (`CUBEJS_DS_ANALYTICS_DB_TYPE`) as the data source `<name>`.
// The name has the spelling of `CUBEJS_DATASOURCES` if that list has it, and is lower case if
// not. It reads no other variable, so it reads no credential.
//
// For a Snowflake data source the Host is the account, if the account is set. The result has
// `default` first, then the other data sources in the order of their names.
func DataSourcesFromEnv(environ []string) []DataSource {
	// The spelling of each name in CUBEJS_DATASOURCES, by its upper case form.
	spelling := map[string]string{}
	values := map[string]map[string]string{}
	set := func(name string, suffix string, value string) {
		if value = strings.TrimSpace(value); value == "" {
			return
		}
		if values[name] == nil {
			values[name] = map[string]string{}
		}
		values[name][suffix] = value
	}
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if key == envDataSourcesList {
			for _, name := range strings.Split(value, ",") {
				if name = strings.TrimSpace(name); name != "" {
					spelling[strings.ToUpper(name)] = name
				}
			}
			continue
		}
		for _, suffix := range envSuffixes {
			if key == envPrefix+suffix {
				set("", suffix, value)
				break
			}
			if strings.HasPrefix(key, envDataSourcePrefix) && strings.HasSuffix(key, "_"+suffix) &&
				len(key) > len(envDataSourcePrefix)+len(suffix)+1 {
				name := strings.TrimSuffix(strings.TrimPrefix(key, envDataSourcePrefix), "_"+suffix)
				if name != "" {
					set(name, suffix, value)
				}
				break
			}
		}
	}

	out := make([]DataSource, 0, len(values))
	for upper, fields := range values {
		name := DefaultDataSource
		if upper != "" {
			name = strings.ToLower(upper)
			if spelled, found := spelling[upper]; found {
				name = spelled
			}
		}
		source := DataSource{
			Name:     name,
			Type:     fields[envSuffixType],
			Database: fields[envSuffixName],
			Host:     fields[envSuffixHost],
			Project:  fields[envSuffixBigQueryProject],
		}
		if account := fields[envSuffixSnowflakeAccount]; account != "" && (source.Type == snowflakeType || source.Host == "") {
			source.Host = account
		}
		out = append(out, source)
	}
	sortDataSources(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseDataSource reads one data source from the text `name=type[,database=..][,host=..][,project=..]`,
// for example `default=snowflake,database=ANALYTICS,host=ab12345.eu-west-1`.
func ParseDataSource(text string) (DataSource, error) {
	parts := strings.Split(text, ",")
	name, kind, found := strings.Cut(parts[0], "=")
	name, kind = strings.TrimSpace(name), strings.TrimSpace(kind)
	if !found || name == "" || kind == "" {
		return DataSource{}, fmt.Errorf("cube: the data source %q does not start with name=type", text)
	}
	source := DataSource{Name: name, Type: kind}
	for _, part := range parts[1:] {
		key, value, found := strings.Cut(part, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || value == "" {
			return DataSource{}, fmt.Errorf("cube: the data source %q has the part %q, which is not key=value", text, part)
		}
		switch key {
		case dataSourceKeyDB:
			source.Database = value
		case dataSourceKeyHost:
			source.Host = value
		case dataSourceKeyProject:
			source.Project = value
		default:
			return DataSource{}, fmt.Errorf("cube: the data source %q has the key %q: use database, host or project", text, key)
		}
	}
	return source, nil
}

// DataSources returns the data sources that the Metadata API of Cube Cloud lists, from
// `GET /v1/data-sources`. Cube Cloud lists only the data sources that connect. Cube Core
// answers with status 404.
func (c *Client) DataSources(ctx context.Context) ([]DataSource, error) {
	body, err := c.do(ctx, http.MethodGet, dataSourcesPath, nil)
	if err != nil {
		return nil, fmt.Errorf("cube: GET %s: %w", dataSourcesPath, err)
	}
	var response struct {
		Data *struct {
			DataSources []struct {
				Name       string `json:"name"`
				Type       string `json:"type"`
				Database   string `json:"database"`
				Properties struct {
					ProjectID string `json:"project_id"`
				} `json:"properties"`
			} `json:"data_sources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("cube: GET %s: the response is not JSON: %w", dataSourcesPath, err)
	}
	if response.Data == nil {
		return nil, errors.New("cube: GET " + dataSourcesPath + ": the response has no \"data\" property")
	}
	var out []DataSource
	for _, source := range response.Data.DataSources {
		if source.Name == "" {
			continue
		}
		out = append(out, DataSource{
			Name:     source.Name,
			Type:     source.Type,
			Database: source.Database,
			Project:  source.Properties.ProjectID,
		})
	}
	return out, nil
}

// mergeDataSources puts the lists together. A later list wins for a name that two lists have.
// It returns the data sources and the origin of each name.
func mergeDataSources(lists ...originList) ([]DataSource, map[string]DataSourceOrigin) {
	byName := map[string]DataSource{}
	origins := map[string]DataSourceOrigin{}
	for _, list := range lists {
		for _, source := range list.sources {
			if source.Name == "" {
				continue
			}
			source.Host = hostOnly(source.Host)
			byName[source.Name] = source
			origins[source.Name] = list.origin
		}
	}
	out := make([]DataSource, 0, len(byName))
	for _, source := range byName {
		out = append(out, source)
	}
	sortDataSources(out)
	return out, origins
}

// hostOnly returns a host with no scheme, no user name, no path and no port:
// `db.example.com` for `https://db.example.com:5432/analytics`.
func hostOnly(host string) string {
	host = strings.TrimSpace(host)
	if _, rest, found := strings.Cut(host, "://"); found {
		host = rest
	}
	host, _, _ = strings.Cut(host, "/")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.Trim(host, "[]")
}

type originList struct {
	origin  DataSourceOrigin
	sources []DataSource
}

// usedDataSources keeps `default` and the data sources that the members use. If no member has
// a data source, the members do not say what is in use, and each data source stays.
func usedDataSources(sources []DataSource, snapshot *Snapshot) []DataSource {
	used := map[string]bool{}
	for _, name := range snapshot.UsedDataSources() {
		used[name] = true
	}
	var out []DataSource
	for _, source := range sources {
		if len(used) == 0 || used[source.Name] || source.Name == DefaultDataSource {
			out = append(out, source)
		}
	}
	return out
}

// sortDataSources puts `default` first, then the other data sources in the order of their names.
func sortDataSources(sources []DataSource) {
	sort.Slice(sources, func(i, j int) bool {
		if (sources[i].Name == DefaultDataSource) != (sources[j].Name == DefaultDataSource) {
			return sources[i].Name == DefaultDataSource
		}
		return sources[i].Name < sources[j].Name
	})
}
