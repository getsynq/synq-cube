package quality

import (
	"testing"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	qualityoauth "github.com/getsynq/quality-oauth-go"
	"github.com/stretchr/testify/require"
)

func TestIsLocal(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":         true,
		"127.0.0.1":         true,
		"::1":               true,
		"developer.synq.io": false,
		"10.0.0.1":          false,
		"":                  false,
	} {
		require.Equal(t, want, isLocal(host), host)
	}
}

func TestNewUploaderNeedsAToken(t *testing.T) {
	_, err := NewUploader(t.Context(), qualityoauth.TargetForEndpoint("localhost:8080"), "")
	require.ErrorContains(t, err, qualityoauth.TokenEnvVar)
}

func TestUploadRefusesASnapshotOverTheLimit(t *testing.T) {
	uploader := &Uploader{}
	err := uploader.Upload(t.Context(), &ingestcubev1.IngestSnapshotRequest{Meta: make([]byte, MaxSnapshotSize+1)})
	require.ErrorContains(t, err, "the limit of one upload")
}
