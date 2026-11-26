package cube

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/getsynq/synq-cube/cube/cubemeta"
)

// Filter holds the settings that leave parts of the data model out of a snapshot.
//
// A filter changes the snapshot itself. For Coalesce Quality a snapshot is the whole data model:
// a cube or a view that is not in the snapshot is removed there. So a filter is not a way to
// update a part of the data model.
//
// Collect applies the filter of its Options while it reads Cube. Apply applies a filter to a
// snapshot that exists, for example one that was read from a file. The two give the same
// snapshot, but for two things. DataSources: Collect keeps the data sources that the members
// of its snapshot use, and Apply keeps the list as it is. The SQL text of an entry of GeneratedSQL:
// Collect asks Cube only for the members that stay, and Apply keeps the text of an entry from
// which it removed a member.
type Filter struct {
	// Include holds glob patterns (the syntax of path.Match, case sensitive) for the names of
	// the cubes and the views that the snapshot holds. Empty means all of them.
	Include []string
	// Exclude holds glob patterns for the names of the cubes and the views that the snapshot
	// does not hold. A name that matches an Exclude pattern is left out, also if it matches an
	// Include pattern.
	Exclude []string
	// SkipHidden leaves out the cubes and the views that are not public (`public: false`, or
	// the older `isVisible: false`), and the members of the other cubes and views that are not
	// public.
	SkipHidden bool
	// SkipMemberSQL leaves out the SQL of the members. Collect then sends no `/v1/sql` request.
	// The snapshot has no lineage to the warehouse tables and no data source for each cube.
	SkipMemberSQL bool
	// SkipModelExport leaves out the Cube Cloud model export. Collect then sends no
	// `/v1/model-export` request. The snapshot then has no private cube, view or member: a Cube
	// deployment in production returns them only in the model export.
	SkipModelExport bool
}

// LeftOut says what a filter left out of a snapshot.
type LeftOut struct {
	// ExcludedCubes, ExcludedViews and ExcludedMembers count what Include and Exclude left out.
	// ExcludedMembers counts the measures and the dimensions of those cubes and views.
	ExcludedCubes   int
	ExcludedViews   int
	ExcludedMembers int
	// HiddenCubes, HiddenViews and HiddenMembers count what SkipHidden left out. HiddenMembers
	// counts the measures and the dimensions of those cubes and views, and the measures and the
	// dimensions that are not public in the cubes and the views that stay.
	HiddenCubes   int
	HiddenViews   int
	HiddenMembers int
	// MemberSQLSkipped is true if SkipMemberSQL is set.
	MemberSQLSkipped bool
	// ModelExportSkipped is true if the filter left out the Cube Cloud model export:
	// SkipModelExport is set, or the filter removes a cube, a view or a member, also one that
	// only the model export has. The model export cannot be filtered, so a snapshot with a part
	// removed has none. Such a snapshot has no private cube, view or member, and no SQL of them.
	ModelExportSkipped bool
}

// FilterReport is the result of Apply beside the snapshot.
type FilterReport struct {
	LeftOut
	// Warnings are problems that did not stop the filter. There is one for each view that
	// includes members of a cube that the filter removed.
	Warnings []string
}

// ErrNothingLeft is the error of Collect and Apply if Include, Exclude and SkipHidden leave no
// cube and no view. A snapshot with no cube and no view removes the whole data model from
// Coalesce Quality.
var ErrNothingLeft = errors.New("cube: the include, exclude and skip-hidden settings leave no cube and no view")

// Validate checks the Include and Exclude patterns. Collect and Apply call it. A caller can
// call it first, to fail at startup.
func (f Filter) Validate() error {
	for _, pattern := range f.Include {
		if err := validatePattern(pattern); err != nil {
			return fmt.Errorf("cube: the include pattern %q is not valid: %w", pattern, err)
		}
	}
	for _, pattern := range f.Exclude {
		if err := validatePattern(pattern); err != nil {
			return fmt.Errorf("cube: the exclude pattern %q is not valid: %w", pattern, err)
		}
	}
	return nil
}

// IsZero is true if the filter leaves nothing out.
func (f Filter) IsZero() bool {
	return !f.filtersNames() && !f.SkipMemberSQL && !f.SkipModelExport
}

// Apply returns the snapshot without the parts that the filter leaves out. It does not change
// its input. The result can share the byte slices of the input that the filter did not change.
//
// Apply removes the cubes, the views and the members from DataModel. It removes those members
// from each entry of GeneratedSQL, each entry with no member left, and the entries of
// MemberErrors of those members. If it removes ModelExport, it removes the SQL and the errors of
// the members that only ModelExport has too. The SQL text of an entry stays as it is. It does not decode
// DataModel into typed structs: each property that stays keeps its bytes. If the filter removes
// nothing, DataModel is the DataModel of the input.
//
// Apply returns an error if a pattern is not valid, if DataModel is not a data model, or if the
// filter leaves no cube and no view (ErrNothingLeft).
func (f Filter) Apply(snapshot *Snapshot) (*Snapshot, *FilterReport, error) {
	if err := f.Validate(); err != nil {
		return nil, nil, err
	}
	out := *snapshot
	report := &FilterReport{}

	removed, meta, err := f.applyToMeta(&out, report)
	if err != nil {
		return nil, nil, err
	}
	if removed.any {
		out.GeneratedSQL = keepGeneratedSQL(out.GeneratedSQL, removed)
		out.MemberErrors = keepMemberErrors(out.MemberErrors, removed)
		report.Warnings = lostLineageWarnings(meta, removed.names)
	}
	if out.ModelExport != nil {
		// The model export has the private cubes, views and members, which DataModel does not
		// have. Their SQL is in the snapshot too.
		whole, err := cubemeta.Parse(snapshot.DataModel)
		if err != nil {
			return nil, nil, err
		}
		private := exportOnlyParts(whole, out.ModelExport)
		if f.dropsModelExport(removed) || f.removesExportOnly(private) {
			report.ModelExportSkipped = true
			out.ModelExport = nil
			// Without the model export nothing declares them, so their SQL goes too.
			if names := exportOnlyNames(private); names.any {
				out.GeneratedSQL = keepGeneratedSQL(out.GeneratedSQL, names)
				out.MemberErrors = keepMemberErrors(out.MemberErrors, names)
			}
		}
	}
	if f.SkipMemberSQL {
		report.MemberSQLSkipped = true
		out.GeneratedSQL = nil
		out.MemberErrors = nil
	}
	if len(out.GeneratedSQL) == 0 {
		out.GeneratedSQL = nil
	}
	if len(out.MemberErrors) == 0 {
		out.MemberErrors = nil
	}
	return &out, report, nil
}

// keepMemberErrors removes the entries of the members that were removed. It does not change its
// input.
func keepMemberErrors(entries []MemberError, removed *removedNames) []MemberError {
	kept := make([]MemberError, 0, len(entries))
	for _, member := range entries {
		if !removed.member(member.Member) {
			kept = append(kept, member)
		}
	}
	return kept
}

// keepGeneratedSQL removes the members that were removed from each entry, and each entry with
// no member left. The SQL text of an entry stays as it is: it still has the output column of a
// member that was removed. It does not change its input.
func keepGeneratedSQL(entries []GeneratedSQL, removed *removedNames) []GeneratedSQL {
	kept := make([]GeneratedSQL, 0, len(entries))
	for _, entry := range entries {
		members := make([]GeneratedSQLMember, 0, len(entry.Members))
		for _, member := range entry.Members {
			if !removed.member(member.Member) {
				members = append(members, member)
			}
		}
		if len(members) == 0 {
			continue
		}
		if len(members) < len(entry.Members) {
			entry.Members = members
		}
		kept = append(kept, entry)
	}
	return kept
}

// applyToMeta filters the DataModel of the snapshot in place and fills the counts of the report. It
// returns what it removed and the data model that stays.
func (f Filter) applyToMeta(snapshot *Snapshot, report *FilterReport) (*removedNames, *cubemeta.Meta, error) {
	meta, err := cubemeta.Parse(snapshot.DataModel)
	if err != nil {
		return nil, nil, err
	}
	removed := &removedNames{names: map[string]bool{}, members: map[string]bool{}}
	if !f.filtersNames() {
		return removed, meta, nil
	}
	filtered, err := f.filterMeta(snapshot.DataModel, removed, &report.LeftOut)
	if err != nil {
		return nil, nil, err
	}
	if !removed.any {
		return removed, meta, nil
	}
	if len(removed.names) == len(meta.Cubes) {
		return nil, nil, ErrNothingLeft
	}
	snapshot.DataModel = filtered
	meta, err = cubemeta.Parse(filtered)
	if err != nil {
		return nil, nil, err
	}
	return removed, meta, nil
}

func validatePattern(pattern string) error {
	if pattern == "" {
		return errors.New("the pattern is empty")
	}
	_, err := path.Match(pattern, "")
	return err
}

// filtersNames is true if a setting can remove a cube, a view or a member.
func (f Filter) filtersNames() bool {
	return len(f.Include) > 0 || len(f.Exclude) > 0 || f.SkipHidden
}

// dropsModelExport is true if the snapshot has no model export after the filter.
func (f Filter) dropsModelExport(removed *removedNames) bool {
	return f.SkipModelExport || removed.any
}

// keepsName is true if Include and Exclude keep the name of a cube or a view. The patterns are
// valid: Validate checked them.
func (f Filter) keepsName(name string) bool {
	if len(f.Include) > 0 && !matchesAny(f.Include, name) {
		return false
	}
	return !matchesAny(f.Exclude, name)
}

func matchesAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// removedNames holds what filterMeta removed.
type removedNames struct {
	// any is true if a cube, a view or a member was removed.
	any bool
	// names holds the name of each cube and each view that was removed.
	names map[string]bool
	// members holds the name of each member that was removed from a cube or a view that stays.
	members map[string]bool
}

// member is true if the member (`orders.revenue`) was removed, alone or with its cube.
func (r *removedNames) member(name string) bool {
	if r.members[name] {
		return true
	}
	owner, _, _ := strings.Cut(name, ".")
	return r.names[owner]
}

// filterMeta removes cubes, views and members from the response of `/v1/meta?extended`.
//
// It writes the `cubes` list again from the JSON text of the entries that stay, so each other
// property of the response and of the entries keeps its bytes. If nothing is removed, the
// result is the input.
func (f Filter) filterMeta(body []byte, removed *removedNames, out *LeftOut) ([]byte, error) {
	var failure error
	filtered, err := rewriteArrays(body, map[string]func(json.RawMessage) json.RawMessage{
		"cubes": func(entry json.RawMessage) json.RawMessage {
			c, ok := cubemeta.ParseCube(entry)
			if !ok {
				// An entry that has no name cannot match a pattern. It stays as it is.
				return entry
			}
			members := len(c.Members())
			if !f.keepsName(c.Name) {
				if c.IsView() {
					out.ExcludedViews++
				} else {
					out.ExcludedCubes++
				}
				out.ExcludedMembers += members
				removed.names[c.Name] = true
				removed.any = true
				return nil
			}
			if !f.SkipHidden {
				return entry
			}
			if c.Hidden() {
				if c.IsView() {
					out.HiddenViews++
				} else {
					out.HiddenCubes++
				}
				out.HiddenMembers += members
				removed.names[c.Name] = true
				removed.any = true
				return nil
			}
			rewritten, err := rewriteArrays(entry, map[string]func(json.RawMessage) json.RawMessage{
				"measures":   dropHidden(cubemeta.KindMeasure, removed, &out.HiddenMembers),
				"dimensions": dropHidden(cubemeta.KindDimension, removed, &out.HiddenMembers),
				// A segment has no SQL, so it is not in the count of members.
				"segments": dropHidden(cubemeta.KindSegment, removed, nil),
			})
			if err != nil {
				failure = err
				return entry
			}
			return rewritten
		},
	})
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	return filtered, nil
}

// dropHidden removes the members that are not public.
func dropHidden(kind cubemeta.MemberKind, removed *removedNames, count *int) func(json.RawMessage) json.RawMessage {
	return func(entry json.RawMessage) json.RawMessage {
		member, ok := cubemeta.ParseMember(entry, kind)
		if !ok || !member.Hidden() {
			return entry
		}
		removed.any = true
		if count != nil {
			removed.members[member.Name] = true
			*count++
		}
		return nil
	}
}

// rewriteArrays writes a JSON object again with other elements in some of its arrays. edit maps
// the name of a property to a function. The function gets each element of the array and returns
// the element that takes its place, or nil to remove the element.
//
// The bytes outside the arrays that change stay as they are, with the order of the properties
// and the white space. A property that is not an array stays as it is.
func rewriteArrays(object []byte, edit map[string]func(json.RawMessage) json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("cube: the JSON text is not an object")
	}
	var out bytes.Buffer
	copied := 0
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("cube: the JSON text is not valid: %w", err)
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("cube: the JSON text is not valid: %w", err)
		}
		change := edit[key]
		if change == nil {
			continue
		}
		end := int(decoder.InputOffset())
		start := end - len(value)
		if start < copied || !bytes.Equal(object[start:end], value) {
			return nil, fmt.Errorf("cube: cannot find the property %q in the JSON text", key)
		}
		var elements []json.RawMessage
		if err := json.Unmarshal(value, &elements); err != nil {
			continue
		}
		kept := make([][]byte, 0, len(elements))
		changed := false
		for _, element := range elements {
			replaced := change(element)
			if replaced == nil {
				changed = true
				continue
			}
			if !bytes.Equal(replaced, element) {
				changed = true
			}
			kept = append(kept, replaced)
		}
		if !changed {
			continue
		}
		out.Write(object[copied:start])
		out.WriteByte('[')
		out.Write(bytes.Join(kept, []byte(",")))
		out.WriteByte(']')
		copied = end
	}
	if copied == 0 {
		return object, nil
	}
	out.Write(object[copied:])
	return out.Bytes(), nil
}

// lostLineageWarnings returns one warning for each view that includes members of a cube that
// was removed from the snapshot.
func lostLineageWarnings(meta *cubemeta.Meta, removed map[string]bool) []string {
	var warnings []string
	for _, c := range meta.Cubes {
		if !c.IsView() {
			continue
		}
		seen := map[string]bool{}
		for _, member := range c.Members() {
			source, _, found := strings.Cut(member.AliasMember, ".")
			if !found || !removed[source] || seen[source] {
				continue
			}
			seen[source] = true
			warnings = append(warnings, fmt.Sprintf(
				"the view %s includes members of %s, which is not in the snapshot: the view has no lineage through %s",
				c.Name, source, source,
			))
		}
	}
	return warnings
}
