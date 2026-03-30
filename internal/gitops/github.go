package gitops

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitmemory "github.com/go-git/go-git/v5/storage/memory"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"
)

// GitHubProvider creates pull requests against a GitHub repository.
type GitHubProvider struct{}

// CreatePR clones the repo, commits the file changes on a new branch, pushes,
// and opens a GitHub pull request.
func (p *GitHubProvider) CreatePR(ctx context.Context, req PRRequest) (*PRResult, error) {
	// Clone repo into memory.
	auth := &gogithttp.BasicAuth{Username: "oauth2", Password: req.Token}

	repo, err := gogit.Clone(gogitmemory.NewStorage(), memfs.New(), &gogit.CloneOptions{
		URL:          req.RepoURL,
		Auth:         auth,
		Depth:        1,
		SingleBranch: true,
		ReferenceName: plumbing.NewBranchReferenceName(req.BaseBranch),
	})
	if err != nil {
		return nil, fmt.Errorf("cloning %s: %w", req.RepoURL, err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("getting worktree: %w", err)
	}

	// Create a new branch.
	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(req.BranchName),
		Create: true,
	}); err != nil {
		return nil, fmt.Errorf("creating branch %s: %w", req.BranchName, err)
	}

	// Write each modified file.
	for _, fc := range req.Files {
		dir := filepath.Dir(fc.Path)
		if dir != "." {
			if err := w.Filesystem.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("creating directory %s: %w", dir, err)
			}
		}
		f, err := w.Filesystem.Create(fc.Path)
		if err != nil {
			return nil, fmt.Errorf("creating file %s: %w", fc.Path, err)
		}
		if _, err := f.Write(fc.Content); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("writing file %s: %w", fc.Path, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("closing file %s: %w", fc.Path, err)
		}
		if _, err := w.Add(fc.Path); err != nil {
			return nil, fmt.Errorf("staging file %s: %w", fc.Path, err)
		}
	}

	// Commit.
	if _, err := w.Commit(req.CommitMsg, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "kite-operator",
			Email: "kite@kite.dev",
			When:  time.Now(),
		},
	}); err != nil {
		return nil, fmt.Errorf("committing: %w", err)
	}

	// Push.
	refSpec := gogitconfig.RefSpec(fmt.Sprintf(
		"+refs/heads/%s:refs/heads/%s", req.BranchName, req.BranchName))
	if err := repo.Push(&gogit.PushOptions{
		Auth:     auth,
		RefSpecs: []gogitconfig.RefSpec{refSpec},
	}); err != nil {
		return nil, fmt.Errorf("pushing branch %s: %w", req.BranchName, err)
	}

	// Create the pull request via the GitHub API.
	owner, repoName, err := parseGitHubURL(req.RepoURL)
	if err != nil {
		return nil, err
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: req.Token})
	tc := oauth2.NewClient(ctx, ts)
	ghClient := github.NewClient(tc)

	prReq := &github.NewPullRequest{
		Title:               github.String(req.Title),
		Body:                github.String(req.Body),
		Head:                github.String(req.BranchName),
		Base:                github.String(req.BaseBranch),
		MaintainerCanModify: github.Bool(true),
	}

	pr, _, err := ghClient.PullRequests.Create(ctx, owner, repoName, prReq)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub PR: %w", err)
	}

	// Optionally add labels.
	if len(req.Labels) > 0 {
		_, _, _ = ghClient.Issues.AddLabelsToIssue(ctx, owner, repoName, pr.GetNumber(), req.Labels)
	}

	// Optionally request reviewers.
	if len(req.Reviewers) > 0 {
		reviewersReq := github.ReviewersRequest{Reviewers: req.Reviewers}
		_, _, _ = ghClient.PullRequests.RequestReviewers(ctx, owner, repoName, pr.GetNumber(), reviewersReq)
	}

	// Optionally auto-merge.
	if req.AutoMerge {
		mergeReq := &github.PullRequestOptions{MergeMethod: "squash"}
		_, _, _ = ghClient.PullRequests.Merge(ctx, owner, repoName, pr.GetNumber(), req.CommitMsg, mergeReq)
	}

	return &PRResult{
		URL:    pr.GetHTMLURL(),
		Number: pr.GetNumber(),
		Title:  pr.GetTitle(),
	}, nil
}

// ReadFileFromRepo reads a file from the default branch of the remote repo
// without a full clone (uses the GitHub Contents API).
func (p *GitHubProvider) ReadFileFromRepo(ctx context.Context, repoURL, token, branch, path string) ([]byte, error) {
	owner, repoName, err := parseGitHubURL(repoURL)
	if err != nil {
		return nil, err
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	ghClient := github.NewClient(tc)

	fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repoName, path,
		&github.RepositoryContentGetOptions{Ref: branch})
	if err != nil {
		return nil, fmt.Errorf("reading %s from GitHub: %w", path, err)
	}
	content, err := fileContent.GetContent()
	if err != nil {
		return nil, fmt.Errorf("decoding file content: %w", err)
	}
	return []byte(content), nil
}

// parseGitHubURL extracts the owner and repository name from a GitHub HTTPS URL.
func parseGitHubURL(repoURL string) (owner, repo string, err error) {
	// Expected formats:
	//   https://github.com/owner/repo
	//   https://github.com/owner/repo.git
	const prefix = "https://github.com/"
	if len(repoURL) <= len(prefix) {
		return "", "", fmt.Errorf("invalid GitHub URL: %s", repoURL)
	}
	path := repoURL[len(prefix):]
	path = trimSuffix(path, ".git")
	parts := splitPath(path)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("cannot extract owner/repo from URL: %s", repoURL)
	}
	return parts[0], parts[1], nil
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func splitPath(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			if i > start {
				parts = append(parts, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

