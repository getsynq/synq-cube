package cube

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/getsynq/synq-cube/cube/cubemeta"
)

// DefaultConcurrency is how many `/v1/sql` requests run at the same time.
const DefaultConcurrency = 4

// Options are the settings of Collect.
type Options struct {
	// Concurrency is how many `/v1/sql` requests run at the same time. Zero or less means
	// DefaultConcurrency.
	Concurrency int
	// BatchSize is the largest count of members in one `/v1/sql` request. Collect sends one
	// request for the measures and one for the dimensions of each cube and each view, and it
	// splits a longer list. Zero or less means DefaultBatchSize. With 1, Collect sends one
	// request for each member, and each entry of Snapshot.GeneratedSQL has one member.
	BatchSize int
	// SecurityContextLabel is a name for the security context of the token (`full-read`). It is
	// never the security context itself.
	SecurityContextLabel string
	// CubeVersion is the version of Cube, if the caller knows it. The REST API does not report it.
	CubeVersion string
	// Filter leaves parts of the data model out of the snapshot. Collect sends no `/v1/sql`
	// request for a member that the filter leaves out. The snapshot is the snapshot that
	// Filter.Apply gives for a collection with no filter.
	Filter Filter
	// DataSources says what the data sources of the deployment are, from a setting of the
	// caller. An entry wins over the entry of Cube Cloud and of EnvironmentDataSources with the
	// same name.
	DataSources []DataSource
	// EnvironmentDataSources says what the data sources are, from the environment of Cube. Give
	// the result of DataSourcesFromEnv. An entry of Cube Cloud with the same name wins over it.
	// Collect itself reads no environment variable.
	EnvironmentDataSources []DataSource
	// Git names the git repository that holds the files of the data model. Nil means that the
	// caller does not know it. Collect copies it to the snapshot.
	Git *GitContext
	// ModelPath is the path of the directory of the data model in the repository. Nil means
	// that the caller does not know it. Collect copies it to the snapshot.
	ModelPath *string
	// Now gives the time of the snapshot: Collect calls it one time, before its first request
	// to Cube. Nil means time.Now.
	Now func() time.Time
}

// Report says what a snapshot holds and what was left out of it.
type Report struct {
	// Cubes is the count of cubes. It does not include the views.
	Cubes int
	// Views is the count of views.
	Views int
	// Members is the count of measures and dimensions of all cubes and views.
	Members int
	// MembersWithSQL is the count of members for which Cube generated SQL.
	MembersWithSQL int
	// MemberErrors is the count of members for which Cube did not generate SQL.
	MemberErrors int
	// GroupedMembers is the count of members for which Cube generated SQL only for a query
	// without `ungrouped`.
	GroupedMembers int
	// Statements is the count of SQL statements, the entries of Snapshot.GeneratedSQL.
	Statements int
	// SQLRequests is the count of `/v1/sql` requests that the collection sent, with the
	// requests that it sent again. Describe does not fill it.
	SQLRequests int
	// ModelExport is true if the snapshot has the response of `/v1/model-export`.
	ModelExport bool
	// ModelExportCubes, ModelExportViews and ModelExportMembers count what only the model export
	// has: the cubes, the views, and the measures and the dimensions that are not in the response
	// of `/v1/meta`. These are the private ones (`public: false`). Cubes, Views and Members
	// include them.
	ModelExportCubes   int
	ModelExportViews   int
	ModelExportMembers int
	// LeftOut says what the filter left out. In a report of Collect, ModelExportSkipped is true
	// if the client is a Cube Cloud client and the filter stopped the model export.
	LeftOut
	// DataSourceOrigins says where the description of each data source of the snapshot came
	// from, by the name of the data source. Describe does not fill it.
	DataSourceOrigins map[string]DataSourceOrigin
	// Warnings are problems that did not stop the snapshot.
	Warnings []string
}

// Describe counts what a snapshot holds: the cubes, the views and the members of DataModel and
// those that only ModelExport has, the members with SQL and the grouped members of GeneratedSQL,
// the members of MemberErrors, and the model export. The report has no warnings, no count of
// requests and nothing left out.
//
// Describe returns an error if DataModel is not a data model.
func Describe(snapshot *Snapshot) (*Report, error) {
	meta, err := cubemeta.Parse(snapshot.DataModel)
	if err != nil {
		return nil, err
	}
	report := &Report{ModelExport: snapshot.ModelExport != nil}
	for _, c := range meta.Cubes {
		if c.IsView() {
			report.Views++
		} else {
			report.Cubes++
		}
		report.Members += len(c.Members())
	}
	if snapshot.ModelExport != nil {
		report.addExportOnly(exportOnlyParts(meta, snapshot.ModelExport))
	}
	report.countSQL(snapshot)
	return report, nil
}

// countSQL counts the members with SQL, the members with an error, the grouped members and the
// statements of the snapshot.
func (r *Report) countSQL(snapshot *Snapshot) {
	r.MemberErrors = len(snapshot.MemberErrors)
	r.Statements = len(snapshot.GeneratedSQL)
	for _, generated := range snapshot.GeneratedSQL {
		r.MembersWithSQL += len(generated.Members)
		if !generated.Ungrouped {
			r.GroupedMembers += len(generated.Members)
		}
	}
}

// Collect reads the data model of a Cube deployment and returns it as one snapshot.
//
// It requests `/v1/meta?extended`, then `/v1/sql` for the measures and for the dimensions of
// each cube and each view: one request for at most Options.BatchSize members. The SQL of a
// request is one entry of Snapshot.GeneratedSQL. If Cube returns an error for a request of many
// members, Collect splits the members in two halves and asks again, down to one member. A member
// for which Cube returns an error is in Snapshot.MemberErrors with the error text.
//
// If the client uses a CloudTokenSource, Collect requests `/v1/model-export` before the SQL. A
// Cube deployment in production returns only what is public from `/v1/meta`. The model export has
// each cube, view and member, so Collect requests the SQL of the private ones too. A failure of
// the model export request is a warning, and the snapshot then has only what is public.
//
// The snapshot says what each data source is, if the caller or Cube Cloud says it: the entries
// of Options.DataSources, then the response of `/v1/data-sources` of Cube Cloud, then the
// entries of Options.EnvironmentDataSources. It keeps `default` and the data sources that the
// members use. A failure of the Cube Cloud request is a warning.
//
// The Filter of the options removes cubes, views and members from the snapshot. A view that
// includes members of a removed cube stays, with a warning.
//
// Collect returns an error if a setting is not valid, if the `/v1/meta` request fails, if its
// response is not a data model, if the filter leaves no cube and no view (ErrNothingLeft), or
// if ctx ends.
func Collect(ctx context.Context, client *Client, options Options) (*Snapshot, *Report, error) {
	filter := options.Filter
	if err := filter.Validate(); err != nil {
		return nil, nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	// The time of a snapshot is the time at which the collection started.
	collectedAt := now()
	metaBody, err := client.Meta(ctx)
	if err != nil {
		return nil, nil, err
	}
	snapshot := &Snapshot{
		DataModel:            metaBody,
		Git:                  options.Git,
		ModelPath:            options.ModelPath,
		CollectedAt:          collectedAt,
		CubeVersion:          options.CubeVersion,
		SecurityContextLabel: options.SecurityContextLabel,
	}
	filtered := &FilterReport{}
	removed, meta, err := filter.applyToMeta(snapshot, filtered)
	if err != nil {
		return nil, nil, err
	}

	report := &Report{LeftOut: filtered.LeftOut}
	if removed.any {
		report.Warnings = lostLineageWarnings(meta, removed.names)
	}

	// The model export comes before the SQL: it names the private members, and Cube generates
	// their SQL as for each other member.
	var private []*cubemeta.ExportOnly
	if client.Cloud() && filter.dropsModelExport(removed) {
		report.ModelExportSkipped = true
	} else if client.Cloud() {
		modelExport, warning := collectModelExport(ctx, client)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if warning != "" {
			report.Warnings = append(report.Warnings, warning)
		}
		if modelExport != nil {
			// Nothing was removed from the data model, so meta is the whole response.
			private = exportOnlyParts(meta, modelExport)
			if filter.removesExportOnly(private) {
				private = nil
				report.ModelExportSkipped = true
			} else {
				snapshot.ModelExport = modelExport
				report.ModelExport = true
			}
		}
	}

	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	var batches []memberBatch
	for _, c := range meta.Cubes {
		if c.IsView() {
			report.Views++
		} else {
			report.Cubes++
		}
		report.Members += len(c.Members())
		batches = append(batches, memberBatches(c, batchSize)...)
	}
	report.addExportOnly(private)
	for _, part := range private {
		batches = append(batches, memberBatches(part.Cube, batchSize)...)
	}
	if filter.SkipMemberSQL {
		report.MemberSQLSkipped = true
		batches = nil
	}

	generated, memberErrors, requests, err := collectBatches(ctx, client, batches, options.Concurrency)
	if err != nil {
		return nil, nil, err
	}
	snapshot.GeneratedSQL = generated
	snapshot.MemberErrors = memberErrors
	for _, member := range snapshot.MemberErrors {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: Cube generated no SQL: %s", member.Member, member.Error))
	}
	report.countSQL(snapshot)
	report.SQLRequests = requests

	lists := []originList{{origin: OriginEnvironment, sources: options.EnvironmentDataSources}}
	if client.Cloud() {
		cloud, err := client.DataSources(ctx)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("the data sources of Cube Cloud are not in the snapshot: %s", err))
		}
		lists = append(lists, originList{origin: OriginCubeCloud, sources: cloud})
	}
	lists = append(lists, originList{origin: OriginSetting, sources: options.DataSources})
	merged, origins := mergeDataSources(lists...)
	snapshot.DataSources = usedDataSources(merged, snapshot)
	if len(snapshot.DataSources) > 0 {
		report.DataSourceOrigins = make(map[string]DataSourceOrigin, len(snapshot.DataSources))
		for _, source := range snapshot.DataSources {
			report.DataSourceOrigins[source.Name] = origins[source.Name]
		}
	}

	return snapshot, report, nil
}

// collectBatches asks Cube for the SQL of the batches, at most concurrency requests at the same
// time. It returns the statements and the members with no SQL in the order of the batches, and the
// count of `/v1/sql` requests. It returns an error only if ctx ends.
func collectBatches(ctx context.Context, client *Client, batches []memberBatch, concurrency int) ([]GeneratedSQL, []MemberError, int, error) {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	var requests atomic.Int64
	collector := &batchCollector{client: client}
	results := make([]batchResult, len(batches))
	group, groupCtx := errgroup.WithContext(withSQLRequestCounter(ctx, &requests))
	group.SetLimit(concurrency)
	for i, batch := range batches {
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			results[i] = collector.collect(groupCtx, batch)
			return groupCtx.Err()
		})
	}
	if err := group.Wait(); err != nil {
		return nil, nil, 0, fmt.Errorf("cube: the collection of the member SQL stopped: %w", err)
	}
	var generated []GeneratedSQL
	var memberErrors []MemberError
	for _, result := range results {
		generated = append(generated, result.generated...)
		memberErrors = append(memberErrors, result.errors...)
	}
	return generated, memberErrors, int(requests.Load()), nil
}

// collectModelExport returns the response of `/v1/model-export`, or a warning.
func collectModelExport(ctx context.Context, client *Client) ([]byte, string) {
	body, err := client.ModelExport(ctx)
	if err != nil {
		return nil, fmt.Sprintf(
			"the Cube Cloud model export is not in the snapshot, so the snapshot has no private cube, view or member: %s",
			err,
		)
	}
	var page struct {
		Data       []json.RawMessage `json:"data"`
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Sprintf("the Cube Cloud model export is not in the snapshot: the response is not JSON: %s", err)
	}
	if page.Pagination.Total > len(page.Data) {
		return body, fmt.Sprintf(
			"the Cube Cloud model export has %d of %d entities: the snapshot holds the first page only",
			len(page.Data),
			page.Pagination.Total,
		)
	}
	return body, ""
}
