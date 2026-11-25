package cmd_test

import (
	"path/filepath"
)

func (s *UploadSuite) TestTheGitRepositoryFromTheSettings() {
	s.setCollection()

	err := s.run("upload",
		"--git-url", "https://user:secret-token@github.com/example/analytics.git",
		"--git-branch", "main", "--git-commit", "0123abc", "--model-path", "./cube\\model/")
	s.Require().NoError(err)

	sent := s.service.requests[0]
	s.Equal("https://github.com/example/analytics.git", sent.GetGitContext().GetCloneUrl())
	s.Equal("main", sent.GetGitContext().GetBranch())
	s.Equal("0123abc", sent.GetGitContext().GetCommitSha())
	s.Equal("cube/model", sent.GetModelPath())
	s.Contains(s.stdout.String(),
		"Git repository: https://github.com/example/analytics.git, branch main, commit 0123abc, model path cube/model (from the settings)\n")
	s.NotContains(s.stdout.String()+s.stderr.String(), "secret-token")
}

func (s *UploadSuite) TestTheGitRepositoryFromTheVariablesOfTheTool() {
	s.setCollection()
	s.T().Setenv("CUBE_GIT_URL", "git@github.com:example/analytics.git")
	s.T().Setenv("CUBE_GIT_COMMIT", "0123abc")
	s.T().Setenv("CUBE_MODEL_PATH", "model")
	// The settings win over the variables of a CI system.
	s.environ = []string{"GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=example/other", "GITHUB_REF_NAME=other", "GITHUB_SHA=fff"}

	s.Require().NoError(s.run("upload"))

	sent := s.service.requests[0]
	s.Equal("git@github.com:example/analytics.git", sent.GetGitContext().GetCloneUrl())
	s.Empty(sent.GetGitContext().GetBranch())
	s.Equal("0123abc", sent.GetGitContext().GetCommitSha())
	s.Equal("model", sent.GetModelPath())
}

func (s *UploadSuite) TestTheGitRepositoryFromTheVariablesOfACISystem() {
	s.setCollection()
	s.environ = []string{"GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=example/analytics", "GITHUB_REF_NAME=main", "GITHUB_SHA=0123abc"}

	s.Require().NoError(s.run("upload"))
	sent := s.service.requests[0]
	s.Equal("https://github.com/example/analytics", sent.GetGitContext().GetCloneUrl())
	s.Equal("main", sent.GetGitContext().GetBranch())
	s.Equal("0123abc", sent.GetGitContext().GetCommitSha())
	// The working directory is in no checkout, so no directory of the data model is known.
	s.Nil(sent.ModelPath)
	s.Contains(s.stdout.String(), "Git repository: https://github.com/example/analytics, branch main, commit 0123abc (from the variables of GitHub Actions)\n")

	// A setting of the branch replaces the branch of the CI system.
	s.Require().NoError(s.run("upload", "--git-branch", "release", "--model-path", "model"))
	sent = s.service.requests[1]
	s.Equal("https://github.com/example/analytics", sent.GetGitContext().GetCloneUrl())
	s.Equal("release", sent.GetGitContext().GetBranch())
	s.Equal("model", sent.GetModelPath())

	// --no-git-context stops the read of the variables.
	s.stdout.Reset()
	s.Require().NoError(s.run("upload", "--no-git-context"))
	s.Nil(s.service.requests[2].GitContext)
	s.NotContains(s.stdout.String(), "Git repository")
	s.T().Setenv("CUBE_NO_GIT_CONTEXT", "true")
	s.Require().NoError(s.run("upload"))
	s.Nil(s.service.requests[3].GitContext)
}

func (s *UploadSuite) TestGitSettingsThatAreNotValid() {
	s.setCollection()

	err := s.run("upload", "--git-url", "/srv/secret-token/analytics")
	s.Require().Error(err)
	s.Contains(err.Error(), "--git-url is not the URL of a git repository")
	s.NotContains(err.Error(), "secret-token")

	// A branch or a commit with no repository.
	err = s.run("upload", "--git-branch", "main")
	s.Require().Error(err)
	s.Contains(err.Error(), "--git-url")
	err = s.run("upload", "--git-commit", "0123abc", "--no-git-context")
	s.Require().Error(err)
	s.Contains(err.Error(), "--git-url")

	s.T().Setenv("CUBE_NO_GIT_CONTEXT", "sometimes")
	err = s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "CUBE_NO_GIT_CONTEXT")
	s.Empty(s.service.requests)
}

func (s *UploadSuite) TestInputKeepsTheGitRepositoryOfTheFile() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	file := filepath.Join(s.T().TempDir(), "snapshot.json")
	s.Require().NoError(s.run("upload", "--dry-run", "--output", file,
		"--git-url", "https://github.com/example/analytics", "--git-branch", "main", "--git-commit", "0123abc", "--model-path", "model"))
	s.T().Setenv("CUBE_API_URL", "")
	s.T().Setenv("CUBE_API_SECRET", "")
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
	// With a file, the command does not read the variables of a CI system.
	s.environ = []string{"GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=example/other", "GITHUB_SHA=fff"}
	s.stdout.Reset()

	s.Require().NoError(s.run("upload", "--input", file))
	sent := s.service.requests[0]
	s.Equal("https://github.com/example/analytics", sent.GetGitContext().GetCloneUrl())
	s.Equal("main", sent.GetGitContext().GetBranch())
	s.Equal("0123abc", sent.GetGitContext().GetCommitSha())
	s.Equal("model", sent.GetModelPath())
	s.Contains(s.stdout.String(), "Git repository: https://github.com/example/analytics, branch main, commit 0123abc, model path model (from the snapshot file)\n")

	// A setting replaces one value of the file. The path `.` is the root of the repository:
	// an empty path that is set.
	s.stdout.Reset()
	s.Require().NoError(s.run("upload", "--input", file, "--git-commit", "4567def", "--model-path", "."))
	sent = s.service.requests[1]
	s.Equal("https://github.com/example/analytics", sent.GetGitContext().GetCloneUrl())
	s.Equal("main", sent.GetGitContext().GetBranch())
	s.Equal("4567def", sent.GetGitContext().GetCommitSha())
	s.Require().NotNil(sent.ModelPath)
	s.Empty(sent.GetModelPath())
	s.Contains(s.stdout.String(), "model path the root of the repository")

	err := s.run("upload", "--input", file, "--no-git-context")
	s.Require().Error(err)
	s.Contains(err.Error(), "does not read Cube")
}
