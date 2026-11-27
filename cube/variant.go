package cube

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/getsynq/synq-cube/cube/cubemeta"
)

// MaxVariantLabelLength is the longest label of a variant.
const MaxVariantLabelLength = 50

// variantLabel is the form of a label: Coalesce Quality puts it into the path of an entity.
var variantLabel = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateVariantLabel checks the label of a variant. A label has 1 to MaxVariantLabelLength
// characters: letters, digits, `_` and `-`.
func ValidateVariantLabel(label string) error {
	if label == "" || len(label) > MaxVariantLabelLength || !variantLabel.MatchString(label) {
		return fmt.Errorf(
			"cube: the variant label %q is not valid: use 1 to %d letters, digits, _ or -",
			label, MaxVariantLabelLength,
		)
	}
	return nil
}

// Variant is the data model that Cube compiles for one more security context.
//
// A Cube deployment can compile another data model for each security context
// (`context_to_app_id`). Coalesce Quality reads the snapshot with one security context, the
// default. A variant is the same data model for another security context. Coalesce Quality makes
// one entity for each cube and each view of the variant whose compiled definition is not the one
// of the snapshot (cubemeta.Differing). A cube or a view that is equal adds nothing.
type Variant struct {
	// Label names the security context of the variant (`dg_42`). It is never the security
	// context itself. Coalesce Quality puts it into the path of each entity of the variant, so it
	// must not change. ValidateVariantLabel says what a label can hold.
	Label string
	// DataModel is the response body of `GET /v1/meta?extended` for the security context of the
	// variant (JSON).
	DataModel []byte
	// ModelExport is the response body of `POST /v1/model-export` for the security context of the
	// variant (JSON). Nil means that the variant has no model export.
	ModelExport []byte
	// GeneratedSQL is the SQL of the measures and the dimensions of the cubes and the views that
	// differ from the snapshot, as in Snapshot.GeneratedSQL. A member of a cube or a view that is
	// equal has no entry.
	GeneratedSQL []GeneratedSQL
	// MemberErrors holds the members of the cubes and the views that differ and that have no SQL,
	// as in Snapshot.MemberErrors.
	MemberErrors []MemberError
}

// VariantReport says what CollectVariant read.
type VariantReport struct {
	// Differing has the names of the cubes and the views that differ from the snapshot, in the
	// order of their names.
	Differing []string
	// Members is the count of measures and dimensions of the cubes and the views that differ.
	Members int
	// MembersWithSQL and MemberErrors count the members of GeneratedSQL and of MemberErrors.
	MembersWithSQL int
	MemberErrors   int
	// SQLRequests is the count of `/v1/sql` requests, with the requests that the client sent
	// again.
	SQLRequests int
	// ModelExport is true if the variant has the response of `/v1/model-export`.
	ModelExport bool
	// Warnings are problems that did not stop the variant.
	Warnings []string
}

// ErrVariantModelExport is the error of CollectVariant if the snapshot has a model export and
// the model export of the variant is not read. Without it, the private cubes and views of the
// variant would look equal to the snapshot, and Coalesce Quality would remove their entities.
var ErrVariantModelExport = errors.New("cube: the model export of the variant is not read")

// CollectVariant reads the data model of one more security context and compares it with
// snapshot, the result of Collect with the default security context. client has a token with the
// security context of the variant.
//
// It requests `/v1/meta?extended`, and `/v1/model-export` if snapshot has a model export. It
// compares each cube and each view with the snapshot (cubemeta.Differing). It requests `/v1/sql`
// only for the members of the cubes and the views that differ, in the batches of Collect.
//
// Of the options, CollectVariant uses Concurrency, BatchSize and Filter. The filter leaves out of
// the variant what it leaves out of the snapshot, by the rules of Collect. The model export cannot
// be filtered. If the snapshot has a model export and the filter removes a part of the variant,
// also one that only its model export has, CollectVariant fails with ErrVariantModelExport.
//
// CollectVariant returns an error if the label is not valid, if the `/v1/meta` request fails, if
// its response is not a data model, if the filter leaves no cube and no view, if the model export
// is not read or not usable with the filter (ErrVariantModelExport), or if ctx ends.
func CollectVariant(ctx context.Context, client *Client, snapshot *Snapshot, label string, options Options) (*Variant, *VariantReport, error) {
	if err := ValidateVariantLabel(label); err != nil {
		return nil, nil, err
	}
	filter := options.Filter
	if err := filter.Validate(); err != nil {
		return nil, nil, err
	}
	metaBody, err := client.Meta(ctx)
	if err != nil {
		return nil, nil, err
	}
	collected := &Snapshot{DataModel: metaBody}
	removed, meta, err := filter.applyToMeta(collected, &FilterReport{})
	if err != nil {
		return nil, nil, err
	}
	variant := &Variant{Label: label, DataModel: collected.DataModel}
	report := &VariantReport{}

	if snapshot.ModelExport != nil && !filter.dropsModelExport(removed) {
		modelExport, warning := collectModelExport(ctx, client)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if modelExport == nil {
			return nil, nil, fmt.Errorf("%w: %s", ErrVariantModelExport, warning)
		}
		if warning != "" {
			report.Warnings = append(report.Warnings, warning)
		}
		if filter.removesExportOnly(exportOnlyParts(meta, modelExport)) {
			// The model export cannot be filtered, and without it each private cube of the
			// variant would look equal.
			return nil, nil, fmt.Errorf("%w: the filter leaves out a part that only the model export of the variant has",
				ErrVariantModelExport)
		}
		variant.ModelExport = modelExport
		report.ModelExport = true
	} else if snapshot.ModelExport != nil {
		return nil, nil, fmt.Errorf("%w: the filter leaves out a part of the data model of the variant", ErrVariantModelExport)
	}

	report.Differing, err = cubemeta.Differing(
		cubemeta.Compilation{DataModel: snapshot.DataModel, ModelExport: snapshot.ModelExport},
		cubemeta.Compilation{DataModel: variant.DataModel, ModelExport: variant.ModelExport},
	)
	if err != nil {
		return nil, nil, err
	}
	if filter.SkipMemberSQL || len(report.Differing) == 0 {
		return variant, report, nil
	}

	// The batches of Collect: the members of `/v1/meta` first, then those that only the model
	// export has, so a statement never mixes the two.
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	var batches []memberBatch
	for _, c := range meta.Cubes {
		if !slices.Contains(report.Differing, c.Name) {
			continue
		}
		report.Members += len(c.Members())
		batches = append(batches, memberBatches(c, batchSize)...)
	}
	if variant.ModelExport != nil {
		for _, part := range exportOnlyParts(meta, variant.ModelExport) {
			if !slices.Contains(report.Differing, part.Cube.Name) {
				continue
			}
			report.Members += len(part.Cube.Members())
			batches = append(batches, memberBatches(part.Cube, batchSize)...)
		}
	}
	generated, memberErrors, requests, err := collectBatches(ctx, client, batches, options.Concurrency)
	if err != nil {
		return nil, nil, err
	}
	variant.GeneratedSQL = generated
	variant.MemberErrors = memberErrors
	for _, entry := range generated {
		report.MembersWithSQL += len(entry.Members)
	}
	report.MemberErrors = len(memberErrors)
	report.SQLRequests = requests
	for _, member := range memberErrors {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: Cube generated no SQL: %s", member.Member, member.Error))
	}
	return variant, report, nil
}
