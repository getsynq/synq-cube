package cube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// SecurityContextLabel is a name for the security context of the token (`full-read`). It is
	// never the security context itself.
	SecurityContextLabel string
	// CubeVersion is the version of Cube, if the caller knows it. The REST API does not report it.
	CubeVersion string
	// Filter leaves parts of the data model out of the snapshot. Collect sends no `/v1/sql`
	// request for a member that the filter leaves out. The snapshot is the snapshot that
	// Filter.Apply gives for a collection with no filter.
	Filter Filter
	// Now gives the time of the snapshot. Nil means time.Now.
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
	// MemberErrors is the count of members for which Cube did not generate SQL.
	MemberErrors int
	// GroupedMembers is the count of members for which Cube generated SQL only for a query
	// without `ungrouped`.
	GroupedMembers int
	// ModelExport is true if the snapshot has the response of `/v1/model-export`.
	ModelExport bool
	// LeftOut says what the filter left out. In a report of Collect, ModelExportSkipped is true
	// if the client is a Cube Cloud client and the filter stopped the model export.
	LeftOut
	// Warnings are problems that did not stop the snapshot.
	Warnings []string
}

// Describe counts what a snapshot holds: the cubes, the views and the members of Meta, the
// members with an error and the grouped members of MemberSQL, and the model export. The report
// has no warnings and nothing left out.
//
// Describe returns an error if Meta is not a data model.
func Describe(snapshot *Snapshot) (*Report, error) {
	meta, err := cubemeta.Parse(snapshot.Meta)
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
	for _, member := range snapshot.MemberSQL {
		if member.Error != "" {
			report.MemberErrors++
		} else if !member.Ungrouped {
			report.GroupedMembers++
		}
	}
	return report, nil
}

// Collect reads the data model of a Cube deployment and returns it as one snapshot.
//
// It requests `/v1/meta?extended`, then `/v1/sql` for each measure and each dimension of each
// cube and view. A member for which Cube returns an error stays in the snapshot with the error
// text. If the client uses a CloudTokenSource, Collect requests `/v1/model-export` too, and a
// failure of that request is a warning.
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

	metaBody, err := client.Meta(ctx)
	if err != nil {
		return nil, nil, err
	}
	snapshot := &Snapshot{
		Meta:                 metaBody,
		CollectedAt:          now(),
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
	var targets []memberTarget
	for _, c := range meta.Cubes {
		if c.IsView() {
			report.Views++
		} else {
			report.Cubes++
		}
		for _, member := range c.Members() {
			targets = append(targets, memberTarget{name: member.Name, kind: member.Kind})
		}
	}
	report.Members = len(targets)
	if filter.SkipMemberSQL {
		report.MemberSQLSkipped = true
		targets = nil
	}

	concurrency := options.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	var results []MemberSQL
	if len(targets) > 0 {
		results = make([]MemberSQL, len(targets))
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(concurrency)
	for i, target := range targets {
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			results[i] = memberSQL(groupCtx, client, target)
			return groupCtx.Err()
		})
	}
	if err := group.Wait(); err != nil {
		return nil, nil, fmt.Errorf("cube: the collection of the member SQL stopped: %w", err)
	}
	for _, result := range results {
		if result.Error != "" {
			report.MemberErrors++
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: Cube generated no SQL: %s", result.Member, result.Error))
			continue
		}
		if !result.Ungrouped {
			report.GroupedMembers++
		}
	}
	snapshot.MemberSQL = results

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
			snapshot.ModelExport = modelExport
			report.ModelExport = true
		}
	}
	return snapshot, report, nil
}

type memberTarget struct {
	name string
	kind cubemeta.MemberKind
}

// memberSQL asks Cube for the SQL of one member. It asks for an ungrouped query first. If Cube
// returns an error, it asks again without `ungrouped`.
func memberSQL(ctx context.Context, client *Client, target memberTarget) MemberSQL {
	query := SQLQuery{Ungrouped: true, Limit: 1}
	if target.kind == cubemeta.KindMeasure {
		query.Measures = []string{target.name}
	} else {
		query.Dimensions = []string{target.name}
	}
	result, err := client.SQL(ctx, query)
	if err != nil && ctx.Err() == nil {
		query.Ungrouped = false
		result, err = client.SQL(ctx, query)
	}
	member := MemberSQL{Member: target.name, Ungrouped: query.Ungrouped}
	if err != nil {
		member.Error = errorText(err)
		return member
	}
	member.SQL = result.SQL
	member.DataSource = result.DataSource
	return member
}

// errorText is the error text of Cube, if the failure is a response of Cube.
func errorText(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Message != "" {
		return apiError.Message
	}
	// An empty Error means that the member has SQL, so a failure always has a text.
	if text := err.Error(); text != "" {
		return text
	}
	return "unknown error"
}

// collectModelExport returns the response of `/v1/model-export`, or a warning.
func collectModelExport(ctx context.Context, client *Client) ([]byte, string) {
	body, err := client.ModelExport(ctx)
	if err != nil {
		return nil, fmt.Sprintf("the Cube Cloud model export is not in the snapshot: %s", err)
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
