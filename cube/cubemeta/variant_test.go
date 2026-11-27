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

func TestDiffering(t *testing.T) {
	base := parse(t, `{"cubes":[
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
			require.Equal(t, test.want, cubemeta.Differing(base, parse(t, test.variant)))
		})
	}
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
