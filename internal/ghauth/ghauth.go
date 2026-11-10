// Package ghauth finds the GitHub token and the current repo for the PRs and CI panes.
package ghauth

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// Token is GH_TOKEN / GITHUB_TOKEN, else `gh auth token`. Asking gh (which may already read
// its keychain item) means rebuilt rexdash binaries never trigger a keychain prompt.
func Token() (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := os.Getenv(k); t != "" {
			return t, nil
		}
	}
	for _, gh := range []string{"gh", "/opt/homebrew/bin/gh", "/usr/local/bin/gh"} {
		out, err := exec.Command(gh, "auth", "token").Output()
		if t := strings.TrimSpace(string(out)); err == nil && t != "" {
			return t, nil
		}
	}
	return "", errors.New("no GitHub token: run `gh auth login` or set GH_TOKEN")
}

// Slug is OWNER/REPO from a remote URL or its path ("/Org/repo.git", "git@github.com:Org/repo.git").
func Slug(url string) string {
	s := url
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j:]
		}
	} else if i := strings.Index(s, ":"); i >= 0 && strings.Contains(s[:i], "@") {
		s = s[i+1:]
	}
	return strings.TrimSuffix(strings.TrimPrefix(s, "/"), ".git")
}

// RepoSlug is OWNER/REPO of the origin remote of the repo in the current directory.
func RepoSlug() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", errors.New("no origin remote here; pass -r OWNER/REPO")
	}
	return Slug(strings.TrimSpace(string(out))), nil
}

// Branch is the current branch name, or "".
func Branch() string {
	out, _ := exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD").Output()
	return strings.TrimSpace(string(out))
}
