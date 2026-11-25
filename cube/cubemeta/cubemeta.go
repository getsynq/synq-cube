// Package cubemeta reads the response of `GET /v1/meta?extended` of the Cube REST API.
//
// The reader is lenient. A property that it does not know is not an error, and a property with
// a type that it does not expect reads as the zero value. Each object keeps its JSON text in
// Raw, so a caller can read a property that this package does not model.
package cubemeta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Types of an entry of the `cubes` list.
const (
	TypeCube = "cube"
	TypeView = "view"
)

// MemberKind says which list of a cube a member comes from.
type MemberKind string

const (
	KindMeasure   MemberKind = "measure"
	KindDimension MemberKind = "dimension"
	KindSegment   MemberKind = "segment"
)

// Meta is the compiled data model of a Cube deployment.
type Meta struct {
	// Cubes holds the cubes and the views, in the order of the response.
	Cubes []*Cube
}

// Cube is one cube or one view.
type Cube struct {
	Name        string
	Type        string
	Title       string
	Description string
	// Public is false for a cube with `public: false`. It is true if the property is absent.
	Public    bool
	IsVisible bool
	// SQL is the query of a cube on a `sql` query. It is empty for a cube on `sql_table`.
	SQL string
	// Extends is the name of the parent cube, if Cube reports it.
	Extends  string
	FileName string
	// ConnectedComponent is the id of the group of cubes that joins connect. Nil if absent.
	ConnectedComponent *int64
	Meta               json.RawMessage

	Measures        []*Member
	Dimensions      []*Member
	Segments        []*Member
	Joins           []*Join
	Hierarchies     []*Hierarchy
	Folders         []*Folder
	NestedFolders   []*Folder
	PreAggregations []*PreAggregation

	Raw json.RawMessage
}

// Hidden is true for a cube or a view that is not public: `public: false`, or the older
// `isVisible: false`.
func (c *Cube) Hidden() bool {
	return !c.Public || !c.IsVisible
}

// IsView is true for a view.
func (c *Cube) IsView() bool {
	return c.Type == TypeView
}

// Members returns the measures, then the dimensions. It does not return the segments.
func (c *Cube) Members() []*Member {
	if len(c.Measures)+len(c.Dimensions) == 0 {
		return nil
	}
	members := make([]*Member, 0, len(c.Measures)+len(c.Dimensions))
	members = append(members, c.Measures...)
	members = append(members, c.Dimensions...)
	return members
}

// Member is a measure, a dimension or a segment.
type Member struct {
	Kind MemberKind
	// Name is the name with the cube or the view (`orders.revenue`).
	Name        string
	Title       string
	ShortTitle  string
	Description string
	// Type is the type of the value (`number`, `string`, `time`, `boolean`).
	Type string
	// AggType is the aggregation of a measure (`count`, `sum`, `number`).
	AggType string
	// SQL is the expression of the member in the data model, with the member references. Cube
	// returns the text between backticks.
	SQL string
	// AliasMember is the cube member behind a member of a view (`orders.revenue`).
	AliasMember string
	Public      bool
	IsVisible   bool
	PrimaryKey  bool
	Cumulative  bool
	// Format is a JSON string (`"currency"`) or a JSON object.
	Format        json.RawMessage
	Meta          json.RawMessage
	Granularities []*Granularity
	DrillMembers  []string

	Raw json.RawMessage
}

// Hidden is true for a member that is not public: `public: false`, or the older
// `isVisible: false`.
func (m *Member) Hidden() bool {
	return !m.Public || !m.IsVisible
}

// ShortName is the name without the cube or the view (`revenue`).
func (m *Member) ShortName() string {
	_, short, found := strings.Cut(m.Name, ".")
	if !found {
		return m.Name
	}
	return short
}

// Granularity is a custom granularity of a time dimension.
type Granularity struct {
	Name     string
	Title    string
	Interval string
	Offset   string
	Origin   string
	Raw      json.RawMessage
}

// Join is a join of a cube to another cube.
type Join struct {
	// Name is the name of the joined cube.
	Name         string
	Relationship string
	SQL          string
	Raw          json.RawMessage
}

// Hierarchy is a list of dimensions, from the widest level to the narrowest.
type Hierarchy struct {
	Name        string
	Title       string
	Levels      []string
	AliasMember string
	Public      bool
	Raw         json.RawMessage
}

// Folder is a folder of a view. In `folders` each folder holds members only. In `nestedFolders`
// a folder can hold folders.
type Folder struct {
	Name    string
	Members []string
	Folders []*Folder
	Raw     json.RawMessage
}

// PreAggregation is a pre-aggregation of a cube.
type PreAggregation struct {
	Name                 string
	Type                 string
	Granularity          string
	PartitionGranularity string
	// The references are the text that Cube returns (`[CUBE.status]`).
	TimeDimensionReference string
	DimensionReferences    string
	MeasureReferences      string
	External               bool
	ScheduledRefresh       bool
	RefreshKey             json.RawMessage
	Indexes                json.RawMessage
	Raw                    json.RawMessage
}

// Parse reads the response body of `GET /v1/meta?extended`.
func Parse(data []byte) (*Meta, error) {
	root, ok := object(data)
	if !ok {
		return nil, fmt.Errorf("cube meta: the response is not a JSON object")
	}
	list, found := root["cubes"]
	if !found {
		return nil, fmt.Errorf("cube meta: the response has no \"cubes\" property")
	}
	items := array(list)
	meta := &Meta{}
	if len(items) == 0 {
		return meta, nil
	}
	meta.Cubes = make([]*Cube, 0, len(items))
	for _, item := range items {
		cube, ok := ParseCube(item)
		if !ok {
			continue
		}
		meta.Cubes = append(meta.Cubes, cube)
	}
	return meta, nil
}

// ParseCube reads one entry of the `cubes` list. It returns false for an entry that is not an
// object or has no name.
func ParseCube(raw json.RawMessage) (*Cube, bool) {
	o, ok := object(raw)
	if !ok {
		return nil, false
	}
	cube := &Cube{
		Name:               text(o["name"]),
		Type:               text(o["type"]),
		Title:              text(o["title"]),
		Description:        text(o["description"]),
		Public:             boolean(o["public"], true),
		IsVisible:          boolean(o["isVisible"], true),
		SQL:                text(o["sql"]),
		Extends:            text(o["extends"]),
		FileName:           text(o["fileName"]),
		ConnectedComponent: integer(o["connectedComponent"]),
		Meta:               present(o["meta"]),
		Measures:           parseMembers(o["measures"], KindMeasure),
		Dimensions:         parseMembers(o["dimensions"], KindDimension),
		Segments:           parseMembers(o["segments"], KindSegment),
		Raw:                raw,
	}
	if cube.Name == "" {
		return nil, false
	}
	for _, item := range array(o["joins"]) {
		j, ok := object(item)
		if !ok {
			continue
		}
		cube.Joins = append(cube.Joins, &Join{
			Name:         text(j["name"]),
			Relationship: text(j["relationship"]),
			SQL:          text(j["sql"]),
			Raw:          item,
		})
	}
	for _, item := range array(o["hierarchies"]) {
		h, ok := object(item)
		if !ok {
			continue
		}
		cube.Hierarchies = append(cube.Hierarchies, &Hierarchy{
			Name:        text(h["name"]),
			Title:       text(h["title"]),
			Levels:      texts(h["levels"]),
			AliasMember: text(h["aliasMember"]),
			Public:      boolean(h["public"], true),
			Raw:         item,
		})
	}
	cube.Folders = parseFolders(o["folders"])
	cube.NestedFolders = parseFolders(o["nestedFolders"])
	for _, item := range array(o["preAggregations"]) {
		p, ok := object(item)
		if !ok {
			continue
		}
		cube.PreAggregations = append(cube.PreAggregations, &PreAggregation{
			Name:                   text(p["name"]),
			Type:                   text(p["type"]),
			Granularity:            text(p["granularity"]),
			PartitionGranularity:   text(p["partitionGranularity"]),
			TimeDimensionReference: text(p["timeDimensionReference"]),
			DimensionReferences:    text(p["dimensionReferences"]),
			MeasureReferences:      text(p["measureReferences"]),
			External:               boolean(p["external"], false),
			ScheduledRefresh:       boolean(p["scheduledRefresh"], false),
			RefreshKey:             present(p["refreshKey"]),
			Indexes:                present(p["indexes"]),
			Raw:                    item,
		})
	}
	return cube, true
}

func parseMembers(raw json.RawMessage, kind MemberKind) []*Member {
	items := array(raw)
	if len(items) == 0 {
		return nil
	}
	members := make([]*Member, 0, len(items))
	for _, item := range items {
		if member, ok := ParseMember(item, kind); ok {
			members = append(members, member)
		}
	}
	return members
}

// ParseMember reads one entry of the `measures`, `dimensions` or `segments` list of a cube. It
// returns false for an entry that is not an object or has no name.
func ParseMember(raw json.RawMessage, kind MemberKind) (*Member, bool) {
	o, ok := object(raw)
	if !ok {
		return nil, false
	}
	member := &Member{
		Kind:         kind,
		Name:         text(o["name"]),
		Title:        text(o["title"]),
		ShortTitle:   text(o["shortTitle"]),
		Description:  text(o["description"]),
		Type:         text(o["type"]),
		AggType:      text(o["aggType"]),
		SQL:          text(o["sql"]),
		AliasMember:  text(o["aliasMember"]),
		Public:       boolean(o["public"], true),
		IsVisible:    boolean(o["isVisible"], true),
		PrimaryKey:   boolean(o["primaryKey"], false),
		Cumulative:   boolean(o["cumulative"], false),
		Format:       present(o["format"]),
		Meta:         present(o["meta"]),
		DrillMembers: texts(o["drillMembers"]),
		Raw:          raw,
	}
	if member.Name == "" {
		return nil, false
	}
	for _, granularity := range array(o["granularities"]) {
		g, ok := object(granularity)
		if !ok {
			continue
		}
		member.Granularities = append(member.Granularities, &Granularity{
			Name:     text(g["name"]),
			Title:    text(g["title"]),
			Interval: text(g["interval"]),
			Offset:   text(g["offset"]),
			Origin:   text(g["origin"]),
			Raw:      granularity,
		})
	}
	return member, true
}

func parseFolders(raw json.RawMessage) []*Folder {
	var folders []*Folder
	for _, item := range array(raw) {
		folder, ok := parseFolder(item)
		if ok {
			folders = append(folders, folder)
		}
	}
	return folders
}

func parseFolder(raw json.RawMessage) (*Folder, bool) {
	o, ok := object(raw)
	if !ok {
		return nil, false
	}
	folder := &Folder{Name: text(o["name"]), Raw: raw}
	for _, item := range array(o["members"]) {
		if name := text(item); name != "" {
			folder.Members = append(folder.Members, name)
			continue
		}
		if nested, ok := parseFolder(item); ok {
			folder.Folders = append(folder.Folders, nested)
		}
	}
	return folder, true
}

func object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var o map[string]json.RawMessage
	if err := json.Unmarshal(raw, &o); err != nil || o == nil {
		return nil, false
	}
	return o, true
}

func array(raw json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return items
}

func text(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func texts(raw json.RawMessage) []string {
	var out []string
	for _, item := range array(raw) {
		if s := text(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func boolean(raw json.RawMessage, fallback bool) bool {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil || present(raw) == nil {
		return fallback
	}
	return b
}

func integer(raw json.RawMessage) *int64 {
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil || present(raw) == nil {
		return nil
	}
	return &n
}

// present returns nil for an absent property and for JSON null.
func present(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}
