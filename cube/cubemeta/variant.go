package cubemeta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Compilation is one compiled data model of a Cube deployment: the response of `/v1/meta`
// and, on Cube Cloud, the response of the model export. Nil ModelExport means none.
type Compilation struct {
	DataModel   []byte
	ModelExport []byte
}

// Merged returns the response of `/v1/meta` with what only the model export has (ModelExport.AddTo).
// A nil model export, or one that does not read, adds nothing: Merged then returns dataModel.
func Merged(dataModel, modelExport []byte) []byte {
	if modelExport == nil {
		return dataModel
	}
	export, err := ParseModelExport(modelExport)
	if err != nil {
		return dataModel
	}
	merged, _, err := export.AddTo(dataModel)
	if err != nil {
		return dataModel
	}
	return merged
}

// compiled is a Compilation that Differing reads.
type compiled struct {
	meta *Meta
	// entries has the entry of each cube and view by its name. The first entry of a name wins.
	entries map[string]*Cube
	// tables has the tables that the model export names for each cube, as JSON. `/v1/meta` does
	// not name the table of a cube on `sql_table`, so only the model export shows a change of it.
	tables map[string][]byte
}

func compile(c Compilation) (*compiled, error) {
	meta, err := Parse(Merged(c.DataModel, c.ModelExport))
	if err != nil {
		return nil, err
	}
	out := &compiled{meta: meta, entries: map[string]*Cube{}, tables: map[string][]byte{}}
	for _, entry := range meta.Cubes {
		if _, seen := out.entries[entry.Name]; !seen {
			out.entries[entry.Name] = entry
		}
	}
	if c.ModelExport == nil {
		return out, nil
	}
	export, err := ParseModelExport(c.ModelExport)
	if err != nil {
		return out, nil
	}
	for _, entity := range export.Entities {
		if len(entity.Tables) == 0 {
			continue
		}
		tables, err := json.Marshal(entity.Tables)
		if err == nil {
			out.tables[entity.Name] = tables
		}
	}
	return out, nil
}

// Differing returns the names of the cubes and the views of variant whose compiled definition is
// not the one in base, in the order of their names.
//
// base and variant are two compilations of one data model, for two security contexts. The
// definition of a cube or a view is its whole entry of the `cubes` list, with what only the model
// export has: the members with their SQL and titles, the joins, the includes of a view and each
// other property. Two entries are equal if their JSON values are equal, whatever the order of the
// properties of an object. The tables that the model export names for a cube count too, as
// `/v1/meta` does not name the table of a cube on `sql_table`. Without a model export, a variant
// that changes only the table of such a cube looks equal.
//
// A view also differs if it includes a member of a differing cube, and that member differs or
// only variant has it. The view then reads another definition, although its own entry is equal.
//
// A cube or a view that only variant has is in the result. A cube or a view that only base has
// is not, as variant adds nothing for it.
func Differing(base, variant Compilation) ([]string, error) {
	from, err := compile(base)
	if err != nil {
		return nil, fmt.Errorf("cube meta: the data model of the default security context: %w", err)
	}
	to, err := compile(variant)
	if err != nil {
		return nil, fmt.Errorf("cube meta: the data model of the variant: %w", err)
	}

	differing := map[string]bool{}
	for name, entry := range to.entries {
		other, found := from.entries[name]
		if !found || !sameJSON(other.Raw, entry.Raw) || !bytes.Equal(from.tables[name], to.tables[name]) {
			differing[name] = true
		}
	}
	for name, entry := range to.entries {
		if !entry.IsView() || differing[name] {
			continue
		}
		if includesChangedMember(entry, from, to, differing) {
			differing[name] = true
		}
	}

	names := make([]string, 0, len(differing))
	for name := range differing {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, nil
	}
	return names, nil
}

// includesChangedMember is true if a member of the view stands for a member of a differing cube,
// and that member differs between the two compilations or only variant has it.
func includesChangedMember(view *Cube, from, to *compiled, differing map[string]bool) bool {
	// Members returns a new slice, so the append does not change the view.
	for _, member := range append(view.Members(), view.Segments...) {
		owner, _, found := strings.Cut(member.AliasMember, ".")
		if !found || !differing[owner] || to.entries[owner] == nil || to.entries[owner].IsView() {
			continue
		}
		after := memberRaw(to.entries[owner], member.AliasMember)
		before := memberRaw(from.entries[owner], member.AliasMember)
		if after == nil {
			continue
		}
		if before == nil || !sameJSON(before, after) {
			return true
		}
	}
	return false
}

// memberRaw returns the JSON of a measure, a dimension or a segment of the cube, or nil.
func memberRaw(c *Cube, name string) json.RawMessage {
	if c == nil {
		return nil
	}
	for _, members := range [][]*Member{c.Measures, c.Dimensions, c.Segments} {
		for _, member := range members {
			if member.Name == name {
				return member.Raw
			}
		}
	}
	return nil
}

// sameJSON is true if the two texts hold the same JSON value. A text that is not JSON is equal
// only to the same bytes.
func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	ca, okA := canonical(a)
	cb, okB := canonical(b)
	return okA && okB && bytes.Equal(ca, cb)
}

// canonical writes a JSON value again with the properties of each object in the order of their
// names. Numbers keep their text.
func canonical(raw json.RawMessage) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return out, true
}
