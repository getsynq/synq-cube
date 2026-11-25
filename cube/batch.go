package cube

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"

	"github.com/getsynq/synq-cube/cube/cubemeta"
)

// DefaultBatchSize is the largest count of members in one `/v1/sql` request.
const DefaultBatchSize = 200

// memberBatch holds members of one cube or of one view that are all measures or all dimensions,
// in the order of the data model.
type memberBatch struct {
	kind  cubemeta.MemberKind
	names []string
}

// batchResult is the result of the members of one batch.
type batchResult struct {
	generated []GeneratedSQL
	errors    []MemberError
}

// memberBatches splits the members of one cube or of one view in batches: the measures, then
// the dimensions, each in lists of at most size members.
func memberBatches(c *cubemeta.Cube, size int) []memberBatch {
	var batches []memberBatch
	for _, members := range [][]*cubemeta.Member{c.Measures, c.Dimensions} {
		for start := 0; start < len(members); start += size {
			end := min(start+size, len(members))
			batch := memberBatch{kind: members[start].Kind, names: make([]string, 0, end-start)}
			for _, member := range members[start:end] {
				batch.names = append(batch.names, member.Name)
			}
			batches = append(batches, batch)
		}
	}
	return batches
}

// errNoColumn is the error of a member whose output column the response of Cube does not name.
// Without the name, nothing says which output column of the statement is the member.
var errNoColumn = errors.New("the response of /v1/sql has no entry for the member in aliasNameToMember, so the output column of the member is not known")

// batchCollector asks Cube for the SQL of batches of members.
type batchCollector struct {
	client *Client
	// noColumns is true after Cube answered a query of many members and named the output
	// column of none of them. The collector then sends no more queries of many members.
	noColumns atomic.Bool
}

// collect returns the SQL of the members of a batch, and the members with no SQL.
//
// It sends one ungrouped query for all members. If Cube returns an error for the query, one
// member or more breaks it. The collector then does two steps:
//
//  1. It splits the members in two halves and asks for each half, down to one member. This
//     finds the members for which Cube generates no ungrouped SQL.
//  2. It asks for the members of step 1 in a query without `ungrouped`, and splits that list
//     in the same way. A member that fails alone there has no SQL, and keeps the error text.
//
// So one member for which Cube generates no SQL does not lose the SQL of the others. A member
// gets a query without `ungrouped` only if its ungrouped query failed while it was alone: a
// grouped query can read a pre-aggregation table, so one member that needs it must not change
// the query of the others. A member is alone in two requests at most, an ungrouped one and one
// without `ungrouped`, as in a collection with one request for each member.
//
// A member whose output column the response does not name is collected alone with memberSQL.
// If the response of that query does not name the column, the member has an error.
func (b *batchCollector) collect(ctx context.Context, batch memberBatch) batchResult {
	var result batchResult
	if b.noColumns.Load() {
		b.collectAlone(ctx, batch.kind, batch.names, &result)
		return result
	}

	ungrouped := b.split(ctx, batch.kind, batch.names, true)
	result.generated = ungrouped.generated
	noColumn := ungrouped.noColumn

	if len(ungrouped.failed) > 0 {
		names := make([]string, 0, len(ungrouped.failed))
		for _, failed := range ungrouped.failed {
			names = append(names, failed.Member)
		}
		var grouped splitResult
		if len(ungrouped.generated) == 0 && len(noColumn) == 0 {
			// No member has ungrouped SQL, for example with a token that has no `sql` scope. A
			// query of many members would fail too, so each member gets its own query.
			for _, name := range names {
				grouped.add(b.split(ctx, batch.kind, []string{name}, false))
			}
		} else {
			grouped = b.split(ctx, batch.kind, names, false)
		}
		result.generated = append(result.generated, grouped.generated...)
		result.errors = grouped.failed
		noColumn = append(noColumn, grouped.noColumn...)
	}
	b.collectAlone(ctx, batch.kind, noColumn, &result)

	// The halves and the members that were collected alone come in another order than the
	// data model.
	position := make(map[string]int, len(batch.names))
	for i, name := range batch.names {
		if _, seen := position[name]; !seen {
			position[name] = i
		}
	}
	sort.SliceStable(result.generated, func(i, j int) bool {
		return position[result.generated[i].Members[0].Member] < position[result.generated[j].Members[0].Member]
	})
	sort.SliceStable(result.errors, func(i, j int) bool {
		return position[result.errors[i].Member] < position[result.errors[j].Member]
	})
	return result
}

// splitResult is the result of split.
type splitResult struct {
	// generated holds the statements of the lists that Cube answered.
	generated []GeneratedSQL
	// failed holds each member that has no SQL from a query of it alone.
	failed []MemberError
	// noColumn holds the members of a statement of many members that have no output column.
	noColumn []string
}

func (r *splitResult) add(other splitResult) {
	r.generated = append(r.generated, other.generated...)
	r.failed = append(r.failed, other.failed...)
	r.noColumn = append(r.noColumn, other.noColumn...)
}

// split asks for the SQL of the members in one query. If Cube returns an error, it splits the
// members in two halves and asks for each half, down to one member.
func (b *batchCollector) split(ctx context.Context, kind cubemeta.MemberKind, names []string, ungrouped bool) splitResult {
	var result splitResult
	if ctx.Err() != nil || len(names) == 0 {
		return result
	}
	if len(names) == 1 {
		generated, err := oneMemberSQL(ctx, b.client, kind, names[0], ungrouped)
		if err != nil {
			result.failed = append(result.failed, MemberError{Member: names[0], Error: errorText(err)})
		} else {
			result.generated = append(result.generated, *generated)
		}
		return result
	}
	generated, noColumn, err := b.query(ctx, kind, names, ungrouped)
	if ctx.Err() != nil {
		return result
	}
	if err != nil {
		half := len(names) / 2
		result.add(b.split(ctx, kind, names[:half], ungrouped))
		result.add(b.split(ctx, kind, names[half:], ungrouped))
		return result
	}
	if generated != nil {
		result.generated = append(result.generated, *generated)
	}
	result.noColumn = noColumn
	return result
}

// query sends one query of many members, a `POST /v1/sql` request. It returns the statement
// with the members that have an output column, or nil if no member has one, and the members
// with no output column.
func (b *batchCollector) query(ctx context.Context, kind cubemeta.MemberKind, names []string, ungrouped bool) (*GeneratedSQL, []string, error) {
	query := newSQLQuery(kind, names)
	query.Ungrouped = ungrouped
	response, err := b.client.SQLOfMembers(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	columns := memberColumns(response.AliasNameToMember)
	generated := &GeneratedSQL{SQL: response.SQL, DataSource: response.DataSource, Ungrouped: ungrouped}
	var noColumn []string
	for _, name := range names {
		column, found := columns[name]
		if !found {
			noColumn = append(noColumn, name)
			continue
		}
		generated.Members = append(generated.Members, GeneratedSQLMember{Member: name, Column: column})
	}
	if len(generated.Members) == 0 {
		b.noColumns.Store(true)
		return nil, noColumn, nil
	}
	return generated, noColumn, nil
}

// collectAlone collects each member with memberSQL.
func (b *batchCollector) collectAlone(ctx context.Context, kind cubemeta.MemberKind, names []string, result *batchResult) {
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		generated, memberError := memberSQL(ctx, b.client, kind, name)
		if memberError != nil {
			result.errors = append(result.errors, *memberError)
			continue
		}
		result.generated = append(result.generated, *generated)
	}
}

// memberSQL asks Cube for the SQL of one member. It asks for an ungrouped query first. If that
// gives no SQL, it asks again without `ungrouped`. It returns the SQL, or the error of the
// member.
func memberSQL(ctx context.Context, client *Client, kind cubemeta.MemberKind, name string) (*GeneratedSQL, *MemberError) {
	generated, err := oneMemberSQL(ctx, client, kind, name, true)
	if err != nil && ctx.Err() == nil {
		generated, err = oneMemberSQL(ctx, client, kind, name, false)
	}
	if err != nil {
		return nil, &MemberError{Member: name, Error: errorText(err)}
	}
	return generated, nil
}

// oneMemberSQL sends one query of one member, a `GET /v1/sql` request. The output column of the
// member is the key of the member in the `aliasNameToMember` map of the response, as Cube sent
// it. The name is never built from the name of the member: a cube with `sql_alias` has another
// prefix. A response that does not name the column is an error (errNoColumn).
func oneMemberSQL(ctx context.Context, client *Client, kind cubemeta.MemberKind, name string, ungrouped bool) (*GeneratedSQL, error) {
	query := newSQLQuery(kind, []string{name})
	query.Ungrouped = ungrouped
	response, err := client.SQL(ctx, query)
	if err != nil {
		return nil, err
	}
	column, found := memberColumns(response.AliasNameToMember)[name]
	if !found {
		return nil, errNoColumn
	}
	return &GeneratedSQL{
		SQL:        response.SQL,
		Members:    []GeneratedSQLMember{{Member: name, Column: column}},
		DataSource: response.DataSource,
		Ungrouped:  ungrouped,
	}, nil
}

// memberColumns turns the `aliasNameToMember` map of a response around: it maps each member to
// the name of its output column. If two columns name one member, the first name in the order of
// the alphabet wins, so the result does not change from run to run.
func memberColumns(aliasNameToMember map[string]string) map[string]string {
	columns := make(map[string]string, len(aliasNameToMember))
	for column, member := range aliasNameToMember {
		if column == "" {
			continue
		}
		if current, found := columns[member]; !found || column < current {
			columns[member] = column
		}
	}
	return columns
}

// newSQLQuery returns the ungrouped query of the members.
func newSQLQuery(kind cubemeta.MemberKind, names []string) SQLQuery {
	query := SQLQuery{Ungrouped: true, Limit: 1}
	if kind == cubemeta.KindMeasure {
		query.Measures = names
	} else {
		query.Dimensions = names
	}
	return query
}

// errorText is the error text of Cube, if the failure is a response of Cube.
func errorText(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Message != "" {
		return apiError.Message
	}
	// An empty text would read as no error, so a failure always has a text.
	if text := err.Error(); text != "" {
		return text
	}
	return "the /v1/sql request failed, and Cube gave no error text"
}
