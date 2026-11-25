package gitcontext

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getsynq/synq-cube/cube"
)

func TestSanitizeURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/example/analytics":                    "https://github.com/example/analytics",
		"https://github.com/example/analytics.git":                "https://github.com/example/analytics.git",
		" https://github.com/example/analytics \n":                "https://github.com/example/analytics",
		"https://user:secret-token@github.com/example/analytics":  "https://github.com/example/analytics",
		"https://secret-token@github.com/example/analytics.git":   "https://github.com/example/analytics.git",
		"https://oauth2:secret-token@gitlab.example.com/a/b.git":  "https://gitlab.example.com/a/b.git",
		"https://github.com/example/analytics?token=secret-token": "https://github.com/example/analytics",
		"ssh://git@github.com/example/analytics.git":              "ssh://github.com/example/analytics.git",
		"ssh://git@git.example.com:2222/example/analytics.git":    "ssh://git.example.com:2222/example/analytics.git",
		"git@github.com:example/analytics.git":                    "git@github.com:example/analytics.git",
		"github.com:example/analytics.git":                        "github.com:example/analytics.git",
		"user:secret-token@github.com:example/analytics.git":      "github.com:example/analytics.git",
		// A text that is not the URL of a remote repository is not sent.
		"":                          "",
		"/srv/git/analytics.git":    "",
		"../analytics":              "",
		"file:///srv/git/analytics": "",
		"C:\\repos\\analytics":      "",
		"https://":                  "",
		"://secret-token@x":         "",
	} {
		got := SanitizeURL(raw)
		require.Equal(t, want, got, raw)
		require.NotContains(t, got, "secret-token", raw)
	}
}

func TestDetectFromGitHubActions(t *testing.T) {
	dir := t.TempDir()

	found := Detect(t.Context(), []string{
		"GITHUB_SERVER_URL=https://github.com",
		"GITHUB_REPOSITORY=example/analytics",
		"GITHUB_REF_NAME=main",
		"GITHUB_REF_TYPE=branch",
		"GITHUB_SHA=0123abc",
		// GitHub Actions wins over GitLab CI.
		"CI_PROJECT_URL=https://gitlab.example.com/example/other",
	}, dir)
	require.Equal(t, &cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"}, found.Git)
	require.Equal(t, OriginGitHubActions, found.Origin)
	require.Empty(t, found.ModelPath)

	// A pull request has the source branch in GITHUB_HEAD_REF.
	found = Detect(t.Context(), []string{
		"GITHUB_SERVER_URL=https://github.example.com/",
		"GITHUB_REPOSITORY=example/analytics",
		"GITHUB_REF_NAME=12/merge",
		"GITHUB_HEAD_REF=new-measure",
		"GITHUB_SHA=0123abc",
	}, dir)
	require.Equal(t, &cube.GitContext{CloneURL: "https://github.example.com/example/analytics", Branch: "new-measure", CommitSHA: "0123abc"}, found.Git)

	// The name of a tag is not a branch.
	found = Detect(t.Context(), []string{
		"GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=example/analytics",
		"GITHUB_REF_NAME=v1.2.3", "GITHUB_REF_TYPE=tag", "GITHUB_SHA=0123abc",
	}, dir)
	require.Equal(t, &cube.GitContext{CloneURL: "https://github.com/example/analytics", CommitSHA: "0123abc"}, found.Git)
}

func TestDetectFromGitLabCI(t *testing.T) {
	found := Detect(t.Context(), []string{
		"CI_PROJECT_URL=https://gitlab-ci-token:secret-token@gitlab.example.com/example/analytics",
		"CI_COMMIT_REF_NAME=main",
		"CI_COMMIT_SHA=0123abc",
	}, t.TempDir())
	require.Equal(t, &cube.GitContext{CloneURL: "https://gitlab.example.com/example/analytics", Branch: "main", CommitSHA: "0123abc"}, found.Git)
	require.Equal(t, OriginGitLabCI, found.Origin)

	found = Detect(t.Context(), []string{
		"CI_PROJECT_URL=https://gitlab.example.com/example/analytics", "CI_COMMIT_REF_NAME=v1.2.3", "CI_COMMIT_TAG=v1.2.3", "CI_COMMIT_SHA=0123abc",
	}, t.TempDir())
	require.Empty(t, found.Git.Branch)
}

func TestDetectFindsNothing(t *testing.T) {
	// A directory that is in no checkout, and no variable of a CI system.
	found := Detect(t.Context(), []string{"GITHUB_REPOSITORY=example/analytics", "CI_COMMIT_SHA=0123abc"}, t.TempDir())
	require.Nil(t, found.Git)
	require.Empty(t, found.Origin)
	require.Empty(t, found.ModelPath)

	found = Detect(t.Context(), nil, filepath.Join(t.TempDir(), "no-such-directory"))
	require.Nil(t, found.Git)
}

// newCheckout makes a git checkout with one commit and a remote. It skips the test if the
// machine has no git binary.
func newCheckout(t *testing.T, remote string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := command.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "--quiet", "--initial-branch", "data-model")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cube.js"), []byte("module.exports = {};\n"), 0o600))
	git("add", "cube.js")
	git("commit", "--quiet", "--message", "first")
	if remote != "" {
		git("remote", "add", "origin", remote)
	}
	return dir
}

func TestDetectFromTheCheckout(t *testing.T) {
	dir := newCheckout(t, "https://user:secret-token@github.com/example/analytics.git")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "model", "cubes"), 0o700))

	// From a directory inside the checkout.
	found := Detect(t.Context(), nil, filepath.Join(dir, "model", "cubes"))
	require.NotNil(t, found.Git)
	require.Equal(t, "https://github.com/example/analytics.git", found.Git.CloneURL)
	require.Equal(t, "data-model", found.Git.Branch)
	require.Len(t, found.Git.CommitSHA, 40)
	require.Equal(t, OriginCheckout, found.Origin)
	require.Equal(t, DefaultModelPath, found.ModelPath)

	// The variables of a CI system win over the checkout. The model path is of the checkout.
	found = Detect(t.Context(), []string{"CI_PROJECT_URL=https://gitlab.example.com/example/analytics", "CI_COMMIT_SHA=0123abc"}, dir)
	require.Equal(t, &cube.GitContext{CloneURL: "https://gitlab.example.com/example/analytics", CommitSHA: "0123abc"}, found.Git)
	require.Equal(t, DefaultModelPath, found.ModelPath)
}

func TestDetectFromACheckoutWithNoRemoteAndNoModelDirectory(t *testing.T) {
	dir := newCheckout(t, "")
	found := Detect(t.Context(), nil, dir)
	require.Nil(t, found.Git)
	require.Empty(t, found.ModelPath)

	// A remote that is a path on the disk is not sent.
	found = Detect(t.Context(), nil, newCheckout(t, "/srv/git/analytics.git"))
	require.Nil(t, found.Git)
}
