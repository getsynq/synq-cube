package cube

import (
	"github.com/getsynq/synq-cube/cube/cubemeta"
)

// exportOnlyParts reads the model export and returns what `/v1/meta` does not have, in the order
// of the export. An export that does not read gives nothing.
//
// A Cube deployment in production returns only what is public from `/v1/meta`. The model export
// has each cube, view and member, so the difference is what is private (`public: false`), and
// what a data access policy hides from `/v1/meta`.
func exportOnlyParts(meta *cubemeta.Meta, modelExport []byte) []*cubemeta.ExportOnly {
	export, err := cubemeta.ParseModelExport(modelExport)
	if err != nil {
		return nil
	}
	return export.OnlyInExport(meta)
}

// removesExportOnly is true if the filter leaves out a cube, a view or a member that only the
// model export has. The model export cannot be filtered, so the snapshot then has no model
// export.
func (f Filter) removesExportOnly(parts []*cubemeta.ExportOnly) bool {
	for _, part := range parts {
		if part.Whole && !f.keepsName(part.Cube.Name) {
			return true
		}
		if !f.SkipHidden {
			continue
		}
		if part.Whole && part.Cube.Hidden() {
			return true
		}
		for _, members := range [][]*cubemeta.Member{part.Cube.Measures, part.Cube.Dimensions, part.Cube.Segments} {
			for _, member := range members {
				if member.Hidden() {
					return true
				}
			}
		}
	}
	return false
}

// exportOnlyNames returns the cubes, the views and the members of the parts as removedNames, to
// remove their SQL from a snapshot that loses its model export.
func exportOnlyNames(parts []*cubemeta.ExportOnly) *removedNames {
	names := &removedNames{names: map[string]bool{}, members: map[string]bool{}}
	for _, part := range parts {
		if part.Whole {
			names.names[part.Cube.Name] = true
		}
		for _, member := range part.Cube.Members() {
			names.members[member.Name] = true
		}
		names.any = true
	}
	return names
}

// addExportOnly adds the counts of the parts to the report. As for the other members, it counts
// the measures and the dimensions, and not the segments.
func (r *Report) addExportOnly(parts []*cubemeta.ExportOnly) {
	for _, part := range parts {
		members := len(part.Cube.Members())
		switch {
		case !part.Whole:
		case part.Cube.IsView():
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
