package cubemeta_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getsynq/synq-cube/cube/cubemeta"
)

func parse(t *testing.T, body string) *cubemeta.Meta {
	t.Helper()
	meta, err := cubemeta.Parse([]byte(body))
	require.NoError(t, err)
	return meta
}

func meta(body string) cubemeta.Compilation {
	return cubemeta.Compilation{DataModel: []byte(body)}
}

func differing(t *testing.T, base, variant cubemeta.Compilation) []string {
	t.Helper()
	names, err := cubemeta.Differing(base, variant)
	require.NoError(t, err)
	return names
}

func TestDiffering(t *testing.T) {
	base := meta(`{"cubes":[
		{"name":"orders","type":"cube","measures":[{"name":"orders.count","sql":"COUNT(*)","title":"Count"}]},
		{"name":"customers","type":"cube","measures":[{"name":"customers.count","sql":"COUNT(*)"}]},
		{"name":"only_base","type":"cube"},
		{"name":"overview","type":"view","measures":[{"name":"overview.count","aliasMember":"orders.count"}]}
	]}`)

	for name, test := range map[string]struct {
		variant string
		want    []string
	}{
		"equal": {
			variant: `{"cubes":[
				{"name":"orders","type":"cube","measures":[{"name":"orders.count","sql":"COUNT(*)","title":"Count"}]},
				{"name":"overview","type":"view","measures":[{"name":"overview.count","aliasMember":"orders.count"}]}
			]}`,
		},
		"the order of the properties": {
			variant: `{"cubes":[{"measures":[{"title":"Count","sql":"COUNT(*)","name":"orders.count"}],"type":"cube","name":"orders"}]}`,
		},
		"the SQL of a member": {
			variant: `{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count","sql":"COUNT(id)","title":"Count"}]}]}`,
			want:    []string{"orders"},
		},
		"the title of a member": {
			variant: `{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count","sql":"COUNT(*)","title":"Orders"}]}]}`,
			want:    []string{"orders"},
		},
		"one more member": {
			variant: `{"cubes":[{"name":"orders","type":"cube","measures":[
				{"name":"orders.count","sql":"COUNT(*)","title":"Count"},{"name":"orders.score_7","sql":"AVG(s7)"}]}]}`,
			want: []string{"orders"},
		},
		"the includes of a view": {
			variant: `{"cubes":[{"name":"overview","type":"view","measures":[{"name":"overview.count","aliasMember":"customers.count"}]}]}`,
			want:    []string{"overview"},
		},
		"a join": {
			variant: `{"cubes":[{"name":"customers","type":"cube","measures":[{"name":"customers.count","sql":"COUNT(*)"}],
				"joins":[{"name":"orders","relationship":"one_to_many","sql":"1 = 1"}]}]}`,
			want: []string{"customers"},
		},
		"a view that includes a changed member of its cube": {
			variant: `{"cubes":[
				{"name":"orders","type":"cube","measures":[{"name":"orders.count","sql":"COUNT(id)","title":"Count"}]},
				{"name":"overview","type":"view","measures":[{"name":"overview.count","aliasMember":"orders.count"}]}
			]}`,
			want: []string{"orders", "overview"},
		},
		"a view that includes only unchanged members of a changed cube": {
			variant: `{"cubes":[
				{"name":"orders","type":"cube","measures":[
					{"name":"orders.count","sql":"COUNT(*)","title":"Count"},{"name":"orders.score_7","sql":"AVG(s7)"}]},
				{"name":"overview","type":"view","measures":[{"name":"overview.count","aliasMember":"orders.count"}]}
			]}`,
			want: []string{"orders"},
		},
		"only in the variant, in the order of the names": {
			variant: `{"cubes":[{"name":"zeta","type":"cube"},{"name":"alpha","type":"view"}]}`,
			want:    []string{"alpha", "zeta"},
		},
		"a name two times counts one time": {
			variant: `{"cubes":[{"name":"zeta","type":"cube"},{"name":"zeta","type":"cube"}]}`,
			want:    []string{"zeta"},
		},
		"no cube": {
			variant: `{"cubes":[]}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, test.want, differing(t, base, meta(test.variant)))
		})
	}
}

// `/v1/meta` does not name the table of a cube on `sql_table`. The tables of the model export do.
func TestDifferingReadsTheTablesOfTheModelExport(t *testing.T) {
	dataModel := []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count","sql":"COUNT(*)"}]}]}`)
	export := func(schema string) []byte {
		return []byte(`{"data":[{"name":"orders","type":"cube","measures":[{"name":"count","sql":"COUNT(*)"}],
			"table_references":[{"database":"DB","schema":"` + schema + `","table":"ORDERS"}]}]}`)
	}
	base := cubemeta.Compilation{DataModel: dataModel, ModelExport: export("TENANT_A")}

	require.Empty(t, differing(t, base, cubemeta.Compilation{DataModel: dataModel, ModelExport: export("TENANT_A")}))
	require.Equal(t, []string{"orders"},
		differing(t, base, cubemeta.Compilation{DataModel: dataModel, ModelExport: export("TENANT_B")}))
	// Without the model export, the table is not known, so the cube looks equal.
	require.Empty(t, differing(t, cubemeta.Compilation{DataModel: dataModel}, cubemeta.Compilation{DataModel: dataModel}))
}

// A private cube that only the model export has is compared too.
func TestDifferingReadsThePrivateCubesOfTheModelExport(t *testing.T) {
	dataModel := []byte(`{"cubes":[]}`)
	export := func(sql string) []byte {
		return []byte(`{"data":[{"name":"scores","type":"cube","public":false,"measures":[{"name":"score","sql":"` + sql + `"}]}]}`)
	}
	base := cubemeta.Compilation{DataModel: dataModel, ModelExport: export("AVG(s)")}

	require.Empty(t, differing(t, base, cubemeta.Compilation{DataModel: dataModel, ModelExport: export("AVG(s)")}))
	require.Equal(t, []string{"scores"},
		differing(t, base, cubemeta.Compilation{DataModel: dataModel, ModelExport: export("AVG(s7)")}))
}

func TestDifferingRefusesAResponseThatIsNoDataModel(t *testing.T) {
	_, err := cubemeta.Differing(meta(`{"cubes":[]}`), meta(`[1]`))
	require.Error(t, err)
	_, err = cubemeta.Differing(meta(`[1]`), meta(`{"cubes":[]}`))
	require.Error(t, err)
}

func TestMerged(t *testing.T) {
	dataModel := []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"}]}]}`)

	require.Equal(t, string(dataModel), string(cubemeta.Merged(dataModel, nil)))
	require.Equal(t, string(dataModel), string(cubemeta.Merged(dataModel, []byte(`not json`))))

	merged := parse(t, string(cubemeta.Merged(dataModel, []byte(`{"data":[
		{"name":"orders","type":"cube","measures":[{"name":"count"},{"name":"private_sum","public":false,"agg_type":"sum"}]},
		{"name":"scores","type":"cube","public":false,"measures":[{"name":"score"}]}
	]}`))))
	require.Len(t, merged.Cubes, 2)
	require.Len(t, merged.Cubes[0].Measures, 2)
	require.Equal(t, "scores", merged.Cubes[1].Name)
}
