package cubemeta

import (
	"bytes"
	"encoding/json"
	"sort"
)

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

// Differing returns the names of the cubes and the views of variant whose compiled definition is
// not the one in base, in the order of their names.
//
// base and variant are two compilations of one data model, for two security contexts. Give each
// as Merged returns it, so the private cubes, views and members count too. The definition is the
// whole entry of the `cubes` list: the members with their SQL and titles, the joins, the
// includes of a view and each other property. Two entries are equal if their JSON values are
// equal, whatever the order of the properties of an object.
//
// A cube or a view that only variant has is in the result. A cube or a view that only base has
// is not, as variant adds nothing for it.
func Differing(base, variant *Meta) []string {
	baseEntries := make(map[string]json.RawMessage, len(base.Cubes))
	for _, c := range base.Cubes {
		if _, seen := baseEntries[c.Name]; !seen {
			baseEntries[c.Name] = c.Raw
		}
	}
	seen := map[string]bool{}
	var names []string
	for _, c := range variant.Cubes {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		other, found := baseEntries[c.Name]
		if found && sameJSON(other, c.Raw) {
			continue
		}
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
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
