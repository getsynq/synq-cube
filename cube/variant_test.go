package cube_test

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// editEntry returns the JSON body with fn applied to the entry of the list that has the name.
// list is `cubes` for `/v1/meta` and `data` for the model export.
func (s *FilterSuite) editEntry(body []byte, list, name string, fn func(entry map[string]any)) []byte {
	var root map[string]any
	s.Require().NoError(json.Unmarshal(body, &root))
	entries, ok := root[list].([]any)
	s.Require().True(ok)
	found := false
	for _, item := range entries {
		entry := item.(map[string]any)
		if entry["name"] == name {
			fn(entry)
			found = true
		}
	}
	s.Require().True(found, name)
	out, err := json.Marshal(root)
	s.Require().NoError(err)
	return out
}

func retitle(title string) func(entry map[string]any) {
	return func(entry map[string]any) { entry["title"] = title }
}

func (s *FilterSuite) collectBase(client *cube.Client) *cube.Snapshot {
	snapshot, _, err := cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)
	return snapshot
}

// Only the cubes and the views that differ get `/v1/sql` requests.
func (s *FilterSuite) TestAVariantRequestsTheSQLOfWhatDiffersOnly() {
	client := s.client()
	base := s.collectBase(client)
	before := len(s.server.SQLRequests())

	changed := s.editEntry(cubetest.MetaExtended(), "cubes", "pg_customers", func(entry map[string]any) {
		measures := entry["measures"].([]any)
		measures[0].(map[string]any)["sql"] = "`COUNT(DISTINCT id)`"
	})
	s.server.Meta = s.editEntry(changed, "cubes", "pg_catalog", retitle("Catalog of the tenant"))

	variant, report, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_a", cube.Options{})
	s.Require().NoError(err)
	s.Equal("tenant_a", variant.Label)
	s.Equal(string(s.server.Meta), string(variant.DataModel))
	s.Nil(variant.ModelExport)
	s.Equal([]string{"pg_catalog", "pg_customers"}, report.Differing)
	s.Equal(9, report.Members)
	s.Equal(9, report.MembersWithSQL)
	s.Zero(report.MemberErrors)

	requested := map[string]bool{}
	for _, request := range s.server.SQLRequests()[before:] {
		for _, member := range request.Members {
			owner, _, _ := strings.Cut(member, ".")
			requested[owner] = true
		}
	}
	s.Equal(map[string]bool{"pg_catalog": true, "pg_customers": true}, requested)
	s.Equal(len(s.server.SQLRequests())-before, report.SQLRequests)
	for _, entry := range variant.GeneratedSQL {
		for _, member := range entry.Members {
			s.True(strings.HasPrefix(member.Member, "pg_catalog.") || strings.HasPrefix(member.Member, "pg_customers."), member.Member)
		}
	}
}

// A variant that compiles the same data model adds nothing and requests no SQL.
func (s *FilterSuite) TestAnEqualVariantRequestsNoSQL() {
	client := s.client()
	base := s.collectBase(client)
	before := len(s.server.SQLRequests())

	variant, report, err := cube.CollectVariant(s.T().Context(), client, base, "same", cube.Options{})
	s.Require().NoError(err)
	s.Empty(report.Differing)
	s.Nil(variant.GeneratedSQL)
	s.Nil(variant.MemberErrors)
	s.Len(s.server.SQLRequests(), before)
}

// The order of the properties of an entry is not a difference.
func (s *FilterSuite) TestThePropertyOrderIsNotADifference() {
	client := s.client()
	base := s.collectBase(client)
	// A marshal of a map writes each object with its properties in the order of their names,
	// which is not the order of the recording.
	s.server.Meta = s.editEntry(cubetest.MetaExtended(), "cubes", "orders", func(map[string]any) {})
	s.NotEqual(string(cubetest.MetaExtended()), string(s.server.Meta))

	_, report, err := cube.CollectVariant(s.T().Context(), client, base, "reordered", cube.Options{})
	s.Require().NoError(err)
	s.Empty(report.Differing)
}

// A cube that only the variant has differs. A cube that only the snapshot has does not.
func (s *FilterSuite) TestACubeOfOneSideOnly() {
	client := s.client()
	base := s.collectBase(client)

	var root map[string]any
	s.Require().NoError(json.Unmarshal(cubetest.MetaExtended(), &root))
	var kept []any
	for _, item := range root["cubes"].([]any) {
		entry := item.(map[string]any)
		if entry["name"] == "pg_orders" {
			continue
		}
		if entry["name"] == "bq_events" {
			added := map[string]any{}
			for key, value := range entry {
				added[key] = value
			}
			added["name"] = "bq_events_tenant"
			added["measures"] = []any{}
			added["dimensions"] = []any{}
			added["segments"] = []any{}
			kept = append(kept, added)
		}
		kept = append(kept, entry)
	}
	root["cubes"] = kept
	body, err := json.Marshal(root)
	s.Require().NoError(err)
	s.server.Meta = body

	_, report, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_b", cube.Options{})
	s.Require().NoError(err)
	s.Equal([]string{"bq_events_tenant"}, report.Differing)
}

// The filter of the snapshot leaves the same parts out of the variant.
func (s *FilterSuite) TestTheFilterAppliesToTheVariant() {
	client := s.client()
	filter := cube.Filter{Exclude: []string{"pg_customers"}}
	base, _, err := cube.Collect(s.T().Context(), client, cube.Options{Filter: filter})
	s.Require().NoError(err)
	before := len(s.server.SQLRequests())

	changed := s.editEntry(cubetest.MetaExtended(), "cubes", "pg_customers", retitle("Hidden by the filter"))
	s.server.Meta = s.editEntry(changed, "cubes", "pg_products", retitle("Products of the tenant"))

	variant, report, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_c", cube.Options{Filter: filter})
	s.Require().NoError(err)
	s.Equal([]string{"pg_products"}, report.Differing)
	s.NotContains(string(variant.DataModel), `"pg_customers"`)
	s.Greater(len(s.server.SQLRequests()), before)
}

// With SkipMemberSQL the variant says what differs and requests no SQL.
func (s *FilterSuite) TestSkipMemberSQLAppliesToTheVariant() {
	client := s.client()
	options := cube.Options{Filter: cube.Filter{SkipMemberSQL: true}}
	base, _, err := cube.Collect(s.T().Context(), client, options)
	s.Require().NoError(err)
	s.server.Meta = s.editEntry(cubetest.MetaExtended(), "cubes", "pg_products", retitle("Products of the tenant"))

	variant, report, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_d", options)
	s.Require().NoError(err)
	s.Equal([]string{"pg_products"}, report.Differing)
	s.Nil(variant.GeneratedSQL)
	s.Empty(s.server.SQLRequests())
}

// A private cube of a deployment in production differs only in the model export. The variant
// reads the model export and requests the SQL of the private members that differ.
func (s *FilterSuite) TestAPrivateCubeOfAVariantDiffersInTheModelExport() {
	client := s.productionClient()
	base := s.collectBase(client)
	s.Require().NotNil(base.ModelExport)
	before := len(s.server.SQLRequests())

	s.server.ModelExport = s.editEntry(cubetest.ModelExport(), "data", "product_snapshots", retitle("Snapshots of the tenant"))

	variant, report, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_e", cube.Options{})
	s.Require().NoError(err)
	s.Equal(string(s.server.ModelExport), string(variant.ModelExport))
	s.True(report.ModelExport)
	s.Equal([]string{"product_snapshots"}, report.Differing)
	s.Equal(3, report.Members)

	requested := map[string]bool{}
	for _, request := range s.server.SQLRequests()[before:] {
		for _, member := range request.Members {
			requested[member] = true
		}
	}
	s.Len(requested, 3)
	for member := range requested {
		s.True(strings.HasPrefix(member, "product_snapshots."), member)
	}
}

// A model export that is not read would make each private cube of the variant look equal. The
// variant fails, so Coalesce Quality keeps the entities that it has.
func (s *FilterSuite) TestAVariantWithNoModelExportFails() {
	client := s.productionClient()
	base := s.collectBase(client)
	s.server.ModelExport = nil

	_, _, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_f", cube.Options{})
	s.Require().Error(err)
	s.True(errors.Is(err, cube.ErrVariantModelExport))
}

// A snapshot with no model export gives a variant with no model export, and no request for it.
func (s *FilterSuite) TestAVariantOfASnapshotWithNoModelExportReadsNone() {
	client := s.cloudClient()
	base, _, err := cube.Collect(s.T().Context(), client, cube.Options{Filter: cube.Filter{SkipModelExport: true}})
	s.Require().NoError(err)
	s.Require().Nil(base.ModelExport)

	variant, _, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_g", cube.Options{})
	s.Require().NoError(err)
	s.Nil(variant.ModelExport)
	s.Zero(s.server.Requests()[cubetest.BasePath+"/v1/model-export"])
}

// A label that is not valid stops the variant before the first request.
func (s *FilterSuite) TestAVariantLabelThatIsNotValid() {
	client := s.client()
	base := s.collectBase(client)
	metaRequests := s.server.Requests()[cubetest.BasePath+"/v1/meta"]
	for _, label := range []string{"", "dg 42", "dg@42", "a/b", strings.Repeat("x", cube.MaxVariantLabelLength+1)} {
		_, _, err := cube.CollectVariant(s.T().Context(), client, base, label, cube.Options{})
		s.Error(err, label)
	}
	s.Equal(metaRequests, s.server.Requests()[cubetest.BasePath+"/v1/meta"])
	for _, label := range []string{"dg_42", "EU-west", strings.Repeat("x", cube.MaxVariantLabelLength)} {
		s.NoError(cube.ValidateVariantLabel(label), label)
	}
}

// A failure of `/v1/meta` fails the variant.
func (s *FilterSuite) TestAVariantWhoseMetaFails() {
	client := s.client()
	base := s.collectBase(client)
	s.server.MetaStatus = 500

	_, _, err := cube.CollectVariant(s.T().Context(), client, base, "tenant_h", cube.Options{})
	s.Error(err)
}
