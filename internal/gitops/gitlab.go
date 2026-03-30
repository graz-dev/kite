package gitops

import (
	"context"
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitmemory "github.com/go-git/go-git/v5/storage/memory"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/xanzy/go-gitlab"
	"time"
	"path/filepath"
)

// GitLabProvider creates merge requests against a GitLab repository.
type GitLabProvider struct {
	// BaseURL allows targeting a self-hosted GitLab instance.
	// Leave empty to use gitlab.com.
	BaseURL string
}

// CreatePR clones the repo, commits the file changes, pushes the branch,
// and opens a GitLab merge request.
func (p *GitLabProvider) CreatePR(ctx context.Context, req PRRequest) (*PRResult, error) {
	auth := &gogithttp.BasicAuth{Username: "oauth2", Password: req.Token}

	repo, err := gogit.Clone(gogitmemory.NewStorage(), memfs.New(), &gogit.CloneOptions{
		URL:           req.RepoURL,
		Auth:          auth,
		Depth:         1,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(req.BaseBranch),
	})
	if err != nil {
		return nil, fmt.Errorf("cloning %s: %w", req.RepoURL, err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("getting worktree: %w", err)
	}

	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(req.BranchName),
		Create: true,
	}); err != nil {
		return nil, fmt.Errorf("creating branch %s: %w", req.BranchName, err)
	}

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

	if _, err := w.Commit(req.CommitMsg, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "kite-operator",
			Email: "kite@kite.dev",
			When:  time.Now(),
		},
	}); err != nil {
		return nil, fmt.Errorf("committing: %w", err)
	}

	refSpec := gogitconfig.RefSpec(fmt.Sprintf(
		"+refs/heads/%s:refs/heads/%s", req.BranchName, req.BranchName))
	if err := repo.Push(&gogit.PushOptions{
		Auth:     auth,
		RefSpecs: []gogitconfig.RefSpec{refSpec},
	}); err != nil {
		return nil, fmt.Errorf("pushing branch: %w", err)
	}

	// Create the merge request via the GitLab API.
	projectID, err := parseGitLabProjectID(req.RepoURL)
	if err != nil {
		return nil, err
	}

	var glOptions []gitlab.ClientOptionFunc
	if p.BaseURL != "" {
		glOptions = append(glOptions, gitlab.WithBaseURL(p.BaseURL))
	}
	glClient, err := gitlab.NewClient(req.Token, glOptions...)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}

	mrOpts := &gitlab.CreateMergeRequestOptions{
		Title:              gitlab.Ptr(req.Title),
		Description:        gitlab.Ptr(req.Body),
		SourceBranch:       gitlab.Ptr(req.BranchName),
		TargetBranch:       gitlab.Ptr(req.BaseBranch),
		RemoveSourceBranch: gitlab.Ptr(true),
	}
	if len(req.Labels) > 0 {
		l := gitlab.LabelOptions(req.Labels)
		mrOpts.Labels = &l
	}

	mr, _, err := glClient.MergeRequests.CreateMergeRequest(projectID, mrOpts)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab MR: %w", err)
	}

	// Request reviewers if specified (GitLab uses reviewer_ids, need user lookup).
	// Simplified: skip reviewer lookup for now; users can be added manually.

	// Auto-merge.
	if req.AutoMerge {
		acceptOpts := &gitlab.AcceptMergeRequestOptions{
			MergeCommitMessage: gitlab.Ptr(req.CommitMsg),
		}
		_, _, _ = glClient.MergeRequests.AcceptMergeRequest(projectID, mr.IID, acceptOpts)
	}

	return &PRResult{
		URL:    mr.WebURL,
		Number: mr.IID,
		Title:  mr.Title,
	}, nil
}

// ReadFileFromRepo reads a file via the GitLab repository files API.
func (p *GitLabProvider) ReadFileFromRepo(ctx context.Context, repoURL, token, branch, path string) ([]byte, error) {
	projectID, err := parseGitLabProjectID(repoURL)
	if err != nil {
		return nil, err
	}

	var glOptions []gitlab.ClientOptionFunc
	if p.BaseURL != "" {
		glOptions = append(glOptions, gitlab.WithBaseURL(p.BaseURL))
	}
	glClient, err := gitlab.NewClient(token, glOptions...)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}

	fileOpts := &gitlab.GetFileOptions{Ref: gitlab.Ptr(branch)}
	file, _, err := glClient.RepositoryFiles.GetFile(projectID, path, fileOpts)
	if err != nil {
		return nil, fmt.Errorf("reading %s from GitLab: %w", path, err)
	}
	return []byte(file.Content), nil
}

// parseGitLabProjectID extracts the "namespace/project" path from a GitLab
// HTTPS URL, which is what the go-gitlab client accepts as a project ID.
func parseGitLabProjectID(repoURL string) (string, error) {
	// Remove common prefixes.
	for _, prefix := range []string{"https://gitlab.com/", "https://gitlab."} {
		if idx := strings.Index(repoURL, prefix); idx >= 0 {
			path := repoURL[idx+len(prefix):]
			path = strings.TrimSuffix(path, ".git")
			// If there's a custom host, skip it.
			if slashIdx := strings.Index(path, "/"); slashIdx > 0 && prefix == "https://gitlab." {
				path = path[slashIdx+1:]
			}
			return path, nil
		}
	}
	// Generic: strip scheme + host.
	if idx := strings.Index(repoURL, "://"); idx >= 0 {
		rest := repoURL[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
			path := rest[slashIdx+1:]
			path = strings.TrimSuffix(path, ".git")
			return path, nil
		}
	}
	return "", fmt.Errorf("cannot parse GitLab project ID from URL: %s", repoURL)
}
