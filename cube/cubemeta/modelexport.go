package cubemeta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ModelExport is the response of `POST /v1/model-export` of the Cube Cloud Metadata API.
//
// The export has each cube, view and member of the data model, also those that `/v1/meta` of a
// deployment in production does not return: what is private (`public: false`), and what a data
// access policy hides. It names its properties in another way than `/v1/meta`: in snake case
// (`agg_type`), and a member has its name with no cube (`revenue`).
type ModelExport struct {
	// Entities holds the cubes and the views, in the order of the response. An entry that is in
	// the response two times is here one time.
	Entities []*ExportEntity
}

// ExportEntity is one cube or one view of the model export.
type ExportEntity struct {
	Name  string
	Type  string
	Title string
	// Public is false for a cube with `public: false`. It is true if the property is absent.
	Public bool
	// Calendar is true for a cube with `calendar: true`.
	Calendar bool
	// Tables has the tables that Cube itself reads from the cube.
	Tables []ExportTable

	// A member is in one list only: a member name is unique in a cube, whatever its kind.
	Measures   []*ExportMember
	Dimensions []*ExportMember
	Segments   []*ExportMember

	// Properties has each property of the entity, as Cube returned it.
	Properties map[string]json.RawMessage
	Raw        json.RawMessage
}

// IsView is true for a view.
func (e *ExportEntity) IsView() bool {
	return e.Type == TypeView
}

// ExportTable is one entry of `table_references` of a cube.
type ExportTable struct {
	Database string
	Schema   string
	Table    string
}

// ExportMember is a measure, a dimension or a segment of the model export.
type ExportMember struct {
	Kind MemberKind
	// Name has no cube (`revenue`).
	Name string
	// Public is false for a member with `public: false`. It is true if the property is absent.
	Public bool
	// Properties has each property of the member, as Cube returned it.
	Properties map[string]json.RawMessage
	Raw        json.RawMessage
}

// ParseModelExport reads the response body of `POST /v1/model-export`.
//
// It returns an error for a body that is not a JSON object, and for a `data` property that is not
// a list. An entry with no name is not in the result.
func ParseModelExport(data []byte) (*ModelExport, error) {
	root, ok := object(data)
	if !ok {
		return nil, fmt.Errorf("cube model export: the response is not a JSON object")
	}
	export := &ModelExport{}
	list := present(root["data"])
	if list == nil {
		return export, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(list, &items); err != nil {
		return nil, fmt.Errorf("cube model export: the \"data\" property is not a list")
	}
	seen := map[string]bool{}
	for _, item := range items {
		o, ok := object(item)
		if !ok {
			continue
		}
		entity := &ExportEntity{
			Name:       text(o["name"]),
			Type:       text(o["type"]),
			Title:      text(o["title"]),
			Public:     boolean(o["public"], true),
			Calendar:   boolean(o["calendar"], false),
			Properties: o,
			Raw:        item,
		}
		if entity.Name == "" || seen[entity.Name] {
			continue
		}
		seen[entity.Name] = true
		for _, reference := range array(o["table_references"]) {
			r, ok := object(reference)
			if !ok {
				continue
			}
			entity.Tables = append(entity.Tables, ExportTable{
				Database: text(r["database"]),
				Schema:   text(r["schema"]),
				Table:    text(r["table"]),
			})
		}
		names := map[string]bool{}
		entity.Measures = parseExportMembers(o["measures"], KindMeasure, names)
		entity.Dimensions = parseExportMembers(o["dimensions"], KindDimension, names)
		entity.Segments = parseExportMembers(o["segments"], KindSegment, names)
		export.Entities = append(export.Entities, entity)
	}
	return export, nil
}

func parseExportMembers(raw json.RawMessage, kind MemberKind, names map[string]bool) []*ExportMember {
	var members []*ExportMember
	for _, item := range array(raw) {
		o, ok := object(item)
		if !ok {
			continue
		}
		name := text(o["name"])
		if name == "" || names[name] {
			continue
		}
		names[name] = true
		members = append(members, &ExportMember{
			Kind:       kind,
			Name:       name,
			Public:     boolean(o["public"], true),
			Properties: o,
			Raw:        item,
		})
	}
	return members
}

// ExportOnly is what one cube or one view of the model export has and the response of `/v1/meta`
// does not have.
type ExportOnly struct {
	// Cube is the cube or the view as an entry of `/v1/meta`, with only the members that
	// `/v1/meta` does not have. Each member has its name with the cube (`orders.revenue`), and
	// its Raw is the member as an entry of `/v1/meta`.
	Cube *Cube
	// Whole is true if `/v1/meta` does not have the cube or the view itself. Raw of Cube is then
	// the entry for the `cubes` list.
	Whole bool
}

// Members counts the measures, the dimensions and the segments.
func (p *ExportOnly) Members() int {
	return len(p.Cube.Measures) + len(p.Cube.Dimensions) + len(p.Cube.Segments)
}

// OnlyInExport returns what the model export has and meta does not have, in the order of the
// export. It skips a cube or a view that has all its members in meta.
//
// The response of `/v1/meta` is the source of each cube, view and member that it has: only it has
// the model file, the format of a member and `aliasMember`. So the export never replaces an entry
// of meta.
func (e *ModelExport) OnlyInExport(meta *Meta) []*ExportOnly {
	known := make(map[string]map[string]bool, len(meta.Cubes))
	for _, c := range meta.Cubes {
		names := map[string]bool{}
		for _, members := range [][]*Member{c.Measures, c.Dimensions, c.Segments} {
			for _, member := range members {
				names[member.ShortName()] = true
			}
		}
		known[c.Name] = names
	}
	var parts []*ExportOnly
	for _, entity := range e.Entities {
		names, inMeta := known[entity.Name]
		added := map[string][]json.RawMessage{}
		lists := []struct {
			property string
			members  []*ExportMember
		}{
			{"measures", entity.Measures},
			{"dimensions", entity.Dimensions},
			{"segments", entity.Segments},
		}
		count := 0
		for _, list := range lists {
			// An empty list, not null: the entry of a cube with no member has its three lists.
			added[list.property] = []json.RawMessage{}
			for _, member := range list.members {
				if names[member.Name] {
					continue
				}
				added[list.property] = append(added[list.property], member.metaEntry(entity))
				count++
			}
		}
		if inMeta && count == 0 {
			continue
		}
		entry := entity.metaEntry(added, !inMeta)
		cube, ok := ParseCube(entry)
		if !ok {
			continue
		}
		parts = append(parts, &ExportOnly{Cube: cube, Whole: !inMeta})
	}
	return parts
}

// AddTo returns the response of `/v1/meta` with what only the model export has, and that
// difference. Each added cube, view and member has the properties and the names of `/v1/meta`, so
// a reader of `/v1/meta` reads the result.
//
// The bytes of the response outside the lists that change stay as they are. With nothing to add,
// AddTo returns dataModel itself.
func (e *ModelExport) AddTo(dataModel []byte) ([]byte, []*ExportOnly, error) {
	root, ok := object(dataModel)
	if !ok {
		return nil, nil, fmt.Errorf("cube meta: the response is not a JSON object")
	}
	entries := array(root["cubes"])
	meta := &Meta{}
	position := map[string]int{}
	for i, entry := range entries {
		cube, ok := ParseCube(entry)
		if !ok {
			continue
		}
		if _, taken := position[cube.Name]; !taken {
			position[cube.Name] = i
		}
		meta.Cubes = append(meta.Cubes, cube)
	}
	parts := e.OnlyInExport(meta)
	if len(parts) == 0 {
		return dataModel, nil, nil
	}
	for _, part := range parts {
		if part.Whole {
			entries = append(entries, part.Cube.Raw)
			continue
		}
		i := position[part.Cube.Name]
		o, _ := object(entries[i])
		lists := map[string][]json.RawMessage{}
		for property, members := range map[string][]*Member{
			"measures": part.Cube.Measures, "dimensions": part.Cube.Dimensions, "segments": part.Cube.Segments,
		} {
			if len(members) == 0 {
				continue
			}
			list := array(o[property])
			for _, member := range members {
				list = append(list, member.Raw)
			}
			lists[property] = list
		}
		entry, err := withArrays(entries[i], lists)
		if err != nil {
			return nil, nil, err
		}
		entries[i] = entry
	}
	merged, err := withArrays(dataModel, map[string][]json.RawMessage{"cubes": entries})
	if err != nil {
		return nil, nil, err
	}
	return merged, parts, nil
}

// metaEntry gives the entity as an entry of the `cubes` list of `/v1/meta`, with the given
// members. If whole is false, the entry has only the name, the type and the `public` property.
//
// A whole entry keeps each property of the export that `/v1/meta` has no other name for, so a
// reader of the entry finds `table_references` and `calendar` too.
func (e *ExportEntity) metaEntry(members map[string][]json.RawMessage, whole bool) json.RawMessage {
	entry := map[string]any{}
	if whole {
		for key, value := range e.Properties {
			entry[key] = value
		}
		// The folders of the export have their folders in them, as `nestedFolders` of `/v1/meta`.
		if folders, found := entry["folders"]; found {
			entry["nestedFolders"] = folders
			delete(entry, "folders")
		}
		if joins := array(e.Properties["joins"]); len(joins) > 0 {
			renamed := make([]json.RawMessage, 0, len(joins))
			for _, join := range joins {
				j, ok := object(join)
				if !ok || text(j["cube"]) == "" {
					continue
				}
				// `/v1/meta` names the joined cube `name`.
				j["name"] = j["cube"]
				delete(j, "cube")
				renamed = append(renamed, marshal(j))
			}
			entry["joins"] = renamed
		}
	} else {
		entry["name"] = e.Name
		entry["type"] = e.Type
		if public, found := e.Properties["public"]; found {
			entry["public"] = public
		}
	}
	for property, list := range members {
		entry[property] = list
	}
	return marshal(entry)
}

// metaEntry gives the member as an entry of a member list of `/v1/meta`.
//
// The export has no short title: the title of a member is the title of its cube, a space and the
// short title. It has no `aliasMember` for a member of a view. The SQL of such a member is one
// reference to the member of the cube (`${orders.revenue}`, or `${orders.customers.region}`
// through a join), so the alias comes from it.
func (m *ExportMember) metaEntry(entity *ExportEntity) json.RawMessage {
	entry := make(map[string]any, len(m.Properties)+2)
	for key, value := range m.Properties {
		entry[key] = value
	}
	entry["name"] = entity.Name + "." + m.Name
	for from, to := range map[string]string{"agg_type": "aggType", "is_primary_key": "primaryKey"} {
		if value, found := entry[from]; found {
			entry[to] = value
			delete(entry, from)
		}
	}
	title := text(m.Properties["title"])
	if short, found := strings.CutPrefix(title, entity.Title+" "); found && entity.Title != "" {
		entry["shortTitle"] = short
	}
	if entity.IsView() && m.Kind != KindSegment {
		if alias := aliasOfReference(text(m.Properties["sql"])); alias != "" {
			entry["aliasMember"] = alias
		}
	}
	return marshal(entry)
}

// aliasOfReference returns the member of a cube that the SQL names, if the SQL is one reference
// to a member of a cube and nothing else: `${orders.count}` gives `orders.count`. A reference
// through joins (`${orders.customers.region}`) names the cube of the member last, so it gives
// `customers.region`. Any other SQL gives "".
func aliasOfReference(sql string) string {
	reference := strings.TrimSpace(sql)
	reference = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(reference, "`"), "`"))
	reference, found := strings.CutPrefix(reference, "${")
	if !found {
		return ""
	}
	reference, found = strings.CutSuffix(reference, "}")
	if !found {
		return ""
	}
	parts := strings.Split(reference, ".")
	if len(parts) < 2 {
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '$':
			default:
				return ""
			}
		}
	}
	// A context variable of Cube is no cube.
	switch parts[0] {
	case "FILTER_PARAMS", "SQL_UTILS", "COMPILE_CONTEXT", "SECURITY_CONTEXT":
		return ""
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// withArrays writes a JSON object again with the given lists in place of its properties of those
// names. A property that the object does not have is added at its end, in the order of the names.
//
// The bytes outside those properties stay as they are, with the order of the properties and the
// white space.
func withArrays(object []byte, lists map[string][]json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("cube meta: the JSON text is not an object")
	}
	var out bytes.Buffer
	copied := 0
	properties := 0
	written := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("cube meta: the JSON text is not valid: %w", err)
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("cube meta: the JSON text is not valid: %w", err)
		}
		properties++
		list, found := lists[key]
		if !found || written[key] {
			continue
		}
		end := int(decoder.InputOffset())
		start := end - len(value)
		if start < copied || !bytes.Equal(object[start:end], value) {
			return nil, fmt.Errorf("cube meta: cannot find the property %q in the JSON text", key)
		}
		written[key] = true
		out.Write(object[copied:start])
		writeArray(&out, list)
		copied = end
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("cube meta: the JSON text is not valid: %w", err)
	}
	closing := int(decoder.InputOffset()) - 1
	out.Write(object[copied:closing])
	missing := make([]string, 0, len(lists))
	for key := range lists {
		if !written[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		if properties > 0 {
			out.WriteByte(',')
		}
		properties++
		out.Write(marshal(key))
		out.WriteByte(':')
		writeArray(&out, lists[key])
	}
	out.Write(object[closing:])
	return out.Bytes(), nil
}

func writeArray(out *bytes.Buffer, elements []json.RawMessage) {
	out.WriteByte('[')
	for i, element := range elements {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(element)
	}
	out.WriteByte(']')
}

// marshal encodes a value that is made of JSON values, so it cannot fail. It keeps `<`, `>` and
// `&` as they are, as the SQL of a member has them.
func marshal(value any) json.RawMessage {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return json.RawMessage("null")
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}
