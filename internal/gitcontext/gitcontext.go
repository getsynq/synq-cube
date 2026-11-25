// Package gitcontext finds the git repository that holds the files of the Cube data model: from
// the variables of a CI system, or from the git checkout that the tool runs in.
//
// It never returns a URL with a user name or a password in it.
package gitcontext

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/getsynq/synq-cube/cube"
)

// Origin says where a git context came from.
type Origin string

const (
	// OriginGitHubActions is the variables of GitHub Actions.
	OriginGitHubActions Origin = "the variables of GitHub Actions"
	// OriginGitLabCI is the variables of GitLab CI.
	OriginGitLabCI Origin = "the variables of GitLab CI"
	// OriginCheckout is the git checkout that the tool runs in.
	OriginCheckout Origin = "the git checkout"
)

// DefaultModelPath is the directory of the data model in a new Cube project.
const DefaultModelPath = "model"

// The time limit of one call of the git binary.
const gitTimeout = 5 * time.Second

// Found is what Detect found.
type Found struct {
	// Git is the repository, the branch and the commit. Nil means that nothing was found.
	Git *cube.GitContext
	// Origin says where Git came from.
	Origin Origin
	// ModelPath is DefaultModelPath if the checkout has that directory, else empty.
	ModelPath string
}

// Detect finds the git repository. It reads the variables of GitHub Actions, then the variables
// of GitLab CI, then the git checkout that holds dir. The first place that names a repository
// gives the three values: the URL, the branch and the commit.
//
// environ has the form of os.Environ. dir is a directory, and empty means the working directory.
// Detect reads the checkout with the git binary. If the binary is absent, if dir is in no
// checkout or if a call fails, the checkout gives nothing and Detect reports no error.
func Detect(ctx context.Context, environ []string, dir string) Found {
	env := map[string]string{}
	for _, entry := range environ {
		if name, value, found := strings.Cut(entry, "="); found {
			env[name] = value
		}
	}
	checkout := readCheckout(ctx, dir)
	found := Found{ModelPath: checkout.modelPath}
	switch {
	case env["GITHUB_SERVER_URL"] != "" && env["GITHUB_REPOSITORY"] != "":
		git := &cube.GitContext{
			CloneURL:  SanitizeURL(strings.TrimRight(env["GITHUB_SERVER_URL"], "/") + "/" + env["GITHUB_REPOSITORY"]),
			CommitSHA: env["GITHUB_SHA"],
		}
		// A pull request has the source branch in GITHUB_HEAD_REF. GITHUB_REF_NAME of a tag is
		// the tag, and not a branch.
		if env["GITHUB_HEAD_REF"] != "" {
			git.Branch = env["GITHUB_HEAD_REF"]
		} else if env["GITHUB_REF_TYPE"] != "tag" {
			git.Branch = env["GITHUB_REF_NAME"]
		}
		found.Git, found.Origin = git, OriginGitHubActions
	case env["CI_PROJECT_URL"] != "":
		git := &cube.GitContext{CloneURL: SanitizeURL(env["CI_PROJECT_URL"]), CommitSHA: env["CI_COMMIT_SHA"]}
		if env["CI_COMMIT_TAG"] == "" {
			git.Branch = env["CI_COMMIT_REF_NAME"]
		}
		found.Git, found.Origin = git, OriginGitLabCI
	case checkout.git != nil:
		found.Git, found.Origin = checkout.git, OriginCheckout
	}
	if found.Git != nil && found.Git.CloneURL == "" {
		found.Git, found.Origin = nil, ""
	}
	return found
}

// SanitizeURL returns the URL of a repository with no user name and no password. It returns an
// empty text for a text that it cannot read as a URL of a remote repository, so a text that
// can hold a credential is never sent.
//
// The short SSH form `git@github.com:example/analytics.git` stays as it is: its user name is
// the fixed name of the service, and the form has no place for a password.
func SanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || parsed.Scheme == "file" {
			return ""
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	// The short SSH form: [user@]host:path.
	hostPart, path, found := strings.Cut(raw, ":")
	if !found || path == "" || len(hostPart) < 2 || strings.ContainsAny(hostPart, "/\\ ") {
		return ""
	}
	if at := strings.LastIndex(raw, "@"); at > strings.Index(raw, ":") {
		// The text before the at sign has a colon: a user name with a password.
		return raw[at+1:]
	}
	return raw
}

type checkout struct {
	git       *cube.GitContext
	modelPath string
}

// readCheckout reads the repository of the checkout that holds dir, with the git binary.
func readCheckout(ctx context.Context, dir string) checkout {
	binary, err := exec.LookPath("git")
	if err != nil {
		return checkout{}
	}
	git := func(args ...string) string {
		callCtx, cancel := context.WithTimeout(ctx, gitTimeout)
		defer cancel()
		command := exec.CommandContext(callCtx, binary, args...)
		command.Dir = dir
		// The binary must never wait for a person.
		command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := command.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	root := git("rev-parse", "--show-toplevel")
	if root == "" {
		return checkout{}
	}
	var result checkout
	if info, err := os.Stat(filepath.Join(root, DefaultModelPath)); err == nil && info.IsDir() {
		result.modelPath = DefaultModelPath
	}
	remote := git("config", "--get", "remote.origin.url")
	if remote == "" {
		if names := strings.Fields(git("remote")); len(names) > 0 {
			remote = git("config", "--get", "remote."+names[0]+".url")
		}
	}
	cloneURL := SanitizeURL(remote)
	if cloneURL == "" {
		return result
	}
	result.git = &cube.GitContext{CloneURL: cloneURL, CommitSHA: git("rev-parse", "HEAD")}
	// A detached HEAD has no branch: git then prints "HEAD".
	if branch := git("rev-parse", "--abbrev-ref", "HEAD"); branch != "HEAD" {
		result.git.Branch = branch
	}
	return result
}
