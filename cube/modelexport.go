package cube

import (
	"encoding/json"

	"github.com/getsynq/synq-cube/cube/cubemeta"
)

// exportOnly is what one cube or one view of the Cube Cloud model export has and the response of
// `/v1/meta` does not have.
//
// A Cube deployment in production returns only what is public from `/v1/meta`. The model export
// has each cube, view and member, so the difference is what is private (`public: false`), and
// what a data access policy hides from `/v1/meta`.
type exportOnly struct {
	// cube has the name and the type of the cube or the view, and only the measures and the
	// dimensions that `/v1/meta` does not have. Each member has its name with the cube
	// (`orders.revenue`).
	cube *cubemeta.Cube
	// whole is true if `/v1/meta` does not have the cube or the view itself.
	whole bool
}

// exportOnlyParts reads the model export and returns what `/v1/meta` does not have, in the order
// of the export. It skips a cube or a view that has all its measures and dimensions in `/v1/meta`.
// An export that is not JSON, or has no `data` list, gives nothing.
//
// The export names its properties in another way than `/v1/meta`: a member has its name with no
// cube (`revenue`).
func exportOnlyParts(meta *cubemeta.Meta, modelExport []byte) []exportOnly {
	var page struct {
		Data []struct {
			Name       string         `json:"name"`
			Type       string         `json:"type"`
			Public     *bool          `json:"public"`
			Measures   []exportMember `json:"measures"`
			Dimensions []exportMember `json:"dimensions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(modelExport, &page); err != nil {
		return nil
	}
	known := make(map[string]map[string]bool, len(meta.Cubes))
	for _, c := range meta.Cubes {
		names := map[string]bool{}
		for _, members := range [][]*cubemeta.Member{c.Measures, c.Dimensions, c.Segments} {
			for _, member := range members {
				names[member.ShortName()] = true
			}
		}
		known[c.Name] = names
	}
	var parts []exportOnly
	seen := map[string]bool{}
	for _, entity := range page.Data {
		if entity.Name == "" || seen[entity.Name] {
			continue
		}
		seen[entity.Name] = true
		names, inMeta := known[entity.Name]
		if names == nil {
			names = map[string]bool{}
		}
		public := entity.Public == nil || *entity.Public
		part := exportOnly{
			cube:  &cubemeta.Cube{Name: entity.Name, Type: entity.Type, Public: public, IsVisible: true},
			whole: !inMeta,
		}
		lists := []struct {
			kind    cubemeta.MemberKind
			members []exportMember
			target  *[]*cubemeta.Member
		}{
			{cubemeta.KindMeasure, entity.Measures, &part.cube.Measures},
			{cubemeta.KindDimension, entity.Dimensions, &part.cube.Dimensions},
		}
		for _, list := range lists {
			for _, member := range list.members {
				// A member name is unique in a cube, whatever its kind.
				if member.Name == "" || names[member.Name] {
					continue
				}
				names[member.Name] = true
				*list.target = append(*list.target, &cubemeta.Member{
					Kind:      list.kind,
					Name:      entity.Name + "." + member.Name,
					Public:    member.Public == nil || *member.Public,
					IsVisible: true,
				})
			}
		}
		if part.whole || len(part.cube.Members()) > 0 {
			parts = append(parts, part)
		}
	}
	return parts
}

// exportMember is a measure or a dimension of the model export.
type exportMember struct {
	// Name has no cube (`revenue`).
	Name   string `json:"name"`
	Public *bool  `json:"public"`
}

// removesExportOnly is true if the filter leaves out a cube, a view or a member that only the
// model export has. The model export cannot be filtered, so the snapshot then has no model
// export.
func (f Filter) removesExportOnly(parts []exportOnly) bool {
	for _, part := range parts {
		if part.whole && !f.keepsName(part.cube.Name) {
			return true
		}
		if !f.SkipHidden {
			continue
		}
		if part.whole && part.cube.Hidden() {
			return true
		}
		for _, member := range part.cube.Members() {
			if member.Hidden() {
				return true
			}
		}
	}
	return false
}

// exportOnlyNames returns the cubes, the views and the members of the parts as removedNames, to
// remove their SQL from a snapshot that loses its model export.
func exportOnlyNames(parts []exportOnly) *removedNames {
	names := &removedNames{names: map[string]bool{}, members: map[string]bool{}}
	for _, part := range parts {
		if part.whole {
			names.names[part.cube.Name] = true
		}
		for _, member := range part.cube.Members() {
			names.members[member.Name] = true
		}
		names.any = true
	}
	return names
}

// addExportOnly adds the counts of the parts to the report.
func (r *Report) addExportOnly(parts []exportOnly) {
	for _, part := range parts {
		members := len(part.cube.Members())
		switch {
		case !part.whole:
		case part.cube.IsView():
			r.Views++
			r.ModelExportViews++
		default:
			r.Cubes++
			r.ModelExportCubes++
		}
		r.Members += members
		r.ModelExportMembers += members
	}
}
