package gitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/google/uuid"
)

var (
	repositoryNamePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	branchNamePattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	githubRepositoryPathPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}\.git$`)
)

type RepositoryInput struct {
	Name           string `json:"name"`
	RemoteURL      string `json:"remoteUrl"`
	DefaultBranch  string `json:"defaultBranch"`
	CredentialUUID string `json:"credentialId"`
}

type GitHubRepositoryInput struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"defaultBranch"`
	CredentialUUID string `json:"credentialId"`
}

type RepositoryView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Provider         string     `json:"provider"`
	RemoteURL        string     `json:"remoteUrl"`
	DefaultBranch    string     `json:"defaultBranch"`
	Mode             string     `json:"mode"`
	CredentialID     *string    `json:"credentialId,omitempty"`
	Status           string     `json:"status"`
	LastError        string     `json:"lastError,omitempty"`
	LastFetchedAt    *time.Time `json:"lastFetchedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	WorkspacePresent bool       `json:"workspacePresent"`
}

type RepositoryGitStatus struct {
	RepositoryID string `json:"repositoryId"`
	Branch       string `json:"branch"`
	Head         string `json:"head,omitempty"`
	RemoteHead   string `json:"remoteHead,omitempty"`
	Clean        bool   `json:"clean"`
	Ahead        int    `json:"ahead"`
	Behind       int    `json:"behind"`
	Diverged     bool   `json:"diverged"`
	State        string `json:"state"`
}

type OperationView struct {
	ID           string     `json:"id"`
	RepositoryID string     `json:"repositoryId,omitempty"`
	Type         string     `json:"type"`
	State        string     `json:"state"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	ErrorMessage string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (s *Service) ListRepositories() ([]RepositoryView, error) {
	rows, err := s.store.ListRepositories()
	if err != nil {
		return nil, err
	}
	views := make([]RepositoryView, 0, len(rows))
	for _, row := range rows {
		views = append(views, s.repositoryView(row))
	}
	return views, nil
}

func (s *Service) CreateRepository(ctx context.Context, input RepositoryInput) (RepositoryView, error) {
	if !s.enabled {
		return RepositoryView{}, errors.New("Git synchronization is disabled")
	}
	clean, err := s.validateRepositoryInput(input)
	if err != nil {
		return RepositoryView{}, err
	}
	row := Repository{
		UUID: uuid.NewString(), Name: clean.Name, Provider: "github", RemoteURL: clean.RemoteURL,
		DefaultBranch: clean.DefaultBranch, Mode: "managed", Status: "cloning",
	}
	if clean.CredentialUUID != "" {
		row.CredentialUUID = &clean.CredentialUUID
	}
	if err := s.validateRepositoryCredential(row); err != nil {
		return RepositoryView{}, err
	}
	lock := s.repositoryLock(row.UUID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.SaveRepository(&row); err != nil {
		return RepositoryView{}, err
	}
	err = s.RunRepositoryOperation(ctx, row.UUID, "clone", func(ctx context.Context) error {
		return s.cloneRepository(ctx, row)
	})
	if err != nil {
		row.Status, row.LastError = "error", safeGitError(err)
		_ = s.store.SaveRepository(&row)
		return s.repositoryView(row), err
	}
	now := time.Now().UTC()
	row.Status, row.LastError, row.LastFetchedAt = "ready", "", &now
	if err := s.store.SaveRepository(&row); err != nil {
		return RepositoryView{}, err
	}
	return s.repositoryView(row), nil
}

func (s *Service) CreateGitHubRepository(ctx context.Context, input GitHubRepositoryInput) (RepositoryView, error) {
	if !s.enabled {
		return RepositoryView{}, errors.New("Git synchronization is disabled")
	}
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	if !repositoryNamePattern.MatchString(input.Name) {
		return RepositoryView{}, errors.New("GitHub repository name must use letters, numbers, dots, dashes, or underscores")
	}
	if len(input.Description) > 300 {
		return RepositoryView{}, errors.New("description must be at most 300 characters")
	}
	if input.DefaultBranch == "" {
		input.DefaultBranch = "main"
	}
	credential, err := s.store.GetCredential(input.CredentialUUID)
	if err != nil {
		return RepositoryView{}, err
	}
	if credential.AuthType != AuthHTTPSToken {
		return RepositoryView{}, errors.New("creating a GitHub repository requires an HTTPS token credential")
	}
	payload, err := s.decryptPayload(credential)
	if err != nil {
		return RepositoryView{}, err
	}
	requestBody, _ := json.Marshal(map[string]any{
		"name": input.Name, "description": input.Description, "private": input.Private,
		"auto_init": true,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.githubAPIBase+"/user/repos", bytes.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer "+payload.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return RepositoryView{}, fmt.Errorf("create GitHub repository: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return RepositoryView{}, fmt.Errorf("GitHub repository creation failed (HTTP %d); verify token repository-administration permissions and that the name is available", resp.StatusCode)
	}
	var created struct {
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil {
		return RepositoryView{}, fmt.Errorf("decode created GitHub repository: %w", err)
	}
	if created.DefaultBranch == "" {
		created.DefaultBranch = input.DefaultBranch
	}
	return s.CreateRepository(ctx, RepositoryInput{
		Name: input.Name, RemoteURL: created.CloneURL, DefaultBranch: created.DefaultBranch,
		CredentialUUID: input.CredentialUUID,
	})
}

func (s *Service) FetchRepository(ctx context.Context, id string) (RepositoryGitStatus, error) {
	lock := s.repositoryLock(id)
	lock.Lock()
	defer lock.Unlock()
	return s.fetchRepositoryLocked(ctx, id)
}

func (s *Service) fetchRepositoryLocked(ctx context.Context, id string) (RepositoryGitStatus, error) {
	row, err := s.store.GetRepository(id)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	err = s.RunRepositoryOperation(ctx, id, "fetch", func(ctx context.Context) error {
		repo, err := s.openRepository(row)
		if err != nil {
			return err
		}
		auth, err := s.authForRepository(ctx, row)
		if err != nil {
			return err
		}
		err = repo.FetchContext(ctx, &gitclient.FetchOptions{RemoteName: "origin", Auth: auth, Force: false, Prune: true})
		if errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	})
	if err != nil {
		s.recordRepositoryError(&row, err)
		return RepositoryGitStatus{}, err
	}
	now := time.Now().UTC()
	row.Status, row.LastError, row.LastFetchedAt = "ready", "", &now
	_ = s.store.SaveRepository(&row)
	return s.RepositoryStatus(id)
}

func (s *Service) RepositoryStatus(id string) (RepositoryGitStatus, error) {
	row, err := s.store.GetRepository(id)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	repo, err := s.openRepository(row)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	worktreeStatus, err := worktree.Status()
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	result := RepositoryGitStatus{RepositoryID: id, Branch: row.DefaultBranch, Clean: worktreeStatus.IsClean(), State: "unknown"}
	localRef, err := repo.Reference(plumbing.NewBranchReferenceName(row.DefaultBranch), true)
	if err != nil {
		return result, fmt.Errorf("local branch %q is unavailable: %w", row.DefaultBranch, err)
	}
	result.Head = localRef.Hash().String()
	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", row.DefaultBranch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		result.State = "local-only"
		result.Ahead = 1
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.RemoteHead = remoteRef.Hash().String()
	if localRef.Hash() == remoteRef.Hash() {
		result.State = "up-to-date"
		return result, nil
	}
	localCommit, err := repo.CommitObject(localRef.Hash())
	if err != nil {
		return result, err
	}
	remoteCommit, err := repo.CommitObject(remoteRef.Hash())
	if err != nil {
		return result, err
	}
	bases, err := localCommit.MergeBase(remoteCommit)
	if err != nil || len(bases) == 0 {
		result.State, result.Diverged = "diverged", true
		return result, nil
	}
	base := bases[0].Hash
	result.Ahead = commitDistance(repo, localRef.Hash(), base)
	result.Behind = commitDistance(repo, remoteRef.Hash(), base)
	switch {
	case result.Ahead > 0 && result.Behind > 0:
		result.State, result.Diverged = "diverged", true
	case result.Ahead > 0:
		result.State = "ahead"
	case result.Behind > 0:
		result.State = "behind"
	default:
		result.State = "unknown"
	}
	return result, nil
}

func (s *Service) PullRepository(ctx context.Context, id string) (RepositoryGitStatus, error) {
	lock := s.repositoryLock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.fetchRepositoryLocked(ctx, id); err != nil {
		return RepositoryGitStatus{}, err
	}
	row, err := s.store.GetRepository(id)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	before, err := s.RepositoryStatus(id)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	if !before.Clean {
		return RepositoryGitStatus{}, errors.New("pull refused: repository workspace contains uncommitted changes")
	}
	if before.Diverged || before.Ahead > 0 {
		return RepositoryGitStatus{}, errors.New("pull refused: local and remote history require an explicit conflict decision")
	}
	if before.Behind == 0 {
		return before, nil
	}
	err = s.RunRepositoryOperation(ctx, id, "pull", func(ctx context.Context) error {
		repo, err := s.openRepository(row)
		if err != nil {
			return err
		}
		worktree, err := repo.Worktree()
		if err != nil {
			return err
		}
		auth, err := s.authForRepository(ctx, row)
		if err != nil {
			return err
		}
		err = worktree.PullContext(ctx, &gitclient.PullOptions{
			RemoteName: "origin", ReferenceName: plumbing.NewBranchReferenceName(row.DefaultBranch),
			SingleBranch: true, Auth: auth,
		})
		if errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	})
	if err != nil {
		s.recordRepositoryError(&row, err)
		return RepositoryGitStatus{}, err
	}
	return s.RepositoryStatus(id)
}

func (s *Service) PushRepository(ctx context.Context, id string) (RepositoryGitStatus, error) {
	lock := s.repositoryLock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.fetchRepositoryLocked(ctx, id); err != nil {
		return RepositoryGitStatus{}, err
	}
	row, err := s.store.GetRepository(id)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	before, err := s.RepositoryStatus(id)
	if err != nil {
		return RepositoryGitStatus{}, err
	}
	if !before.Clean {
		return RepositoryGitStatus{}, errors.New("push refused: repository workspace contains uncommitted changes")
	}
	if before.Diverged || before.Behind > 0 {
		return RepositoryGitStatus{}, errors.New("push refused: remote contains commits that are not present locally")
	}
	if before.Ahead == 0 {
		return before, nil
	}
	err = s.RunRepositoryOperation(ctx, id, "push", func(ctx context.Context) error {
		repo, err := s.openRepository(row)
		if err != nil {
			return err
		}
		auth, err := s.authForRepository(ctx, row)
		if err != nil {
			return err
		}
		err = repo.PushContext(ctx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth})
		if errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	})
	if err != nil {
		s.recordRepositoryError(&row, err)
		return RepositoryGitStatus{}, err
	}
	return s.fetchRepositoryLocked(ctx, id)
}

func (s *Service) DeleteRepository(id string) error {
	lock := s.repositoryLock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.store.GetRepository(id); err != nil {
		return err
	}
	hasBindings, err := s.store.RepositoryHasBindings(id)
	if err != nil {
		return err
	}
	if hasBindings {
		return errors.New("repository is still linked to one or more stacks")
	}
	path, err := s.repositoryPath(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove repository workspace: %w", err)
	}
	return s.store.DeleteRepository(id)
}

func (s *Service) ListRepositoryOperations(id string, limit int) ([]OperationView, error) {
	if _, err := s.store.GetRepository(id); err != nil {
		return nil, err
	}
	rows, err := s.store.ListOperations(id, limit)
	if err != nil {
		return nil, err
	}
	views := make([]OperationView, 0, len(rows))
	for _, row := range rows {
		views = append(views, OperationView{ID: row.UUID, RepositoryID: row.RepositoryUUID, Type: row.OperationType, State: row.State, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, ErrorMessage: row.ErrorMessage, CreatedAt: row.CreatedAt})
	}
	return views, nil
}

func (s *Service) validateRepositoryInput(input RepositoryInput) (RepositoryInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.RemoteURL = strings.TrimSpace(input.RemoteURL)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	input.CredentialUUID = strings.TrimSpace(input.CredentialUUID)
	if !repositoryNamePattern.MatchString(input.Name) {
		return input, errors.New("repository name must use letters, numbers, dots, dashes, or underscores")
	}
	if input.DefaultBranch == "" {
		input.DefaultBranch = "main"
	}
	if !branchNamePattern.MatchString(input.DefaultBranch) || strings.Contains(input.DefaultBranch, "..") {
		return input, errors.New("invalid default branch name")
	}
	if err := validateGitHubURL(input.RemoteURL, true); err != nil {
		return input, err
	}
	if input.CredentialUUID != "" {
		if _, err := s.store.GetCredential(input.CredentialUUID); err != nil {
			return input, err
		}
	}
	return input, nil
}

func (s *Service) cloneRepository(ctx context.Context, row Repository) error {
	if err := os.MkdirAll(s.workspaceRoot, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.workspaceRoot, 0o700); err != nil {
		return err
	}
	destination, err := s.repositoryPath(row.UUID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("repository workspace already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := destination + ".tmp-" + uuid.NewString()
	defer os.RemoveAll(temporary)
	auth, err := s.authForRepository(ctx, row)
	if err != nil {
		return err
	}
	_, err = gitclient.PlainCloneContext(ctx, temporary, false, &gitclient.CloneOptions{
		URL: row.RemoteURL, RemoteName: "origin", Auth: auth,
		ReferenceName: plumbing.NewBranchReferenceName(row.DefaultBranch), SingleBranch: true,
	})
	if err != nil {
		return fmt.Errorf("clone repository: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("activate repository workspace: %w", err)
	}
	if err := os.Chmod(destination, 0o700); err != nil {
		return fmt.Errorf("secure repository workspace: %w", err)
	}
	return nil
}

func (s *Service) authForRepository(ctx context.Context, row Repository) (transport.AuthMethod, error) {
	if row.CredentialUUID == nil || *row.CredentialUUID == "" {
		if strings.HasPrefix(row.RemoteURL, "git@") || strings.HasPrefix(row.RemoteURL, "ssh://") {
			return nil, errors.New("SSH repository requires an SSH key credential")
		}
		return nil, nil
	}
	credential, err := s.store.GetCredential(*row.CredentialUUID)
	if err != nil {
		return nil, err
	}
	payload, err := s.decryptPayload(credential)
	if err != nil {
		return nil, err
	}
	switch credential.AuthType {
	case AuthPublic:
		if strings.HasPrefix(row.RemoteURL, "git@") || strings.HasPrefix(row.RemoteURL, "ssh://") {
			return nil, errors.New("public credentials support HTTPS repositories only")
		}
		return nil, nil
	case AuthHTTPSToken:
		if !strings.HasPrefix(row.RemoteURL, "https://") {
			return nil, errors.New("HTTPS token credentials require an HTTPS repository URL")
		}
		username := credential.Username
		if username == "" {
			username = "x-access-token"
		}
		return &githttp.BasicAuth{Username: username, Password: payload.Token}, nil
	case AuthSSHKey:
		if !strings.HasPrefix(row.RemoteURL, "git@github.com:") && !strings.HasPrefix(row.RemoteURL, "ssh://git@github.com/") && !strings.HasPrefix(row.RemoteURL, "ssh://git@github.com:22/") {
			return nil, errors.New("SSH key credentials require a GitHub SSH repository URL")
		}
		key, err := gitssh.NewPublicKeys("git", []byte(payload.PrivateKey), payload.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("load SSH private key: %w", err)
		}
		hostKeys, err := s.githubHostKeys(ctx)
		if err != nil {
			return nil, err
		}
		key.HostKeyCallback = trustedHostKeyCallback(hostKeys)
		return key, nil
	default:
		return nil, errors.New("unsupported repository credential type")
	}
}

func (s *Service) validateRepositoryCredential(row Repository) error {
	isSSH := strings.HasPrefix(row.RemoteURL, "git@") || strings.HasPrefix(row.RemoteURL, "ssh://")
	if row.CredentialUUID == nil || *row.CredentialUUID == "" {
		if isSSH {
			return errors.New("SSH repository requires an SSH key credential")
		}
		return nil
	}
	credential, err := s.store.GetCredential(*row.CredentialUUID)
	if err != nil {
		return err
	}
	switch credential.AuthType {
	case AuthPublic:
		if isSSH {
			return errors.New("public credentials support HTTPS repositories only")
		}
	case AuthHTTPSToken:
		if !strings.HasPrefix(row.RemoteURL, "https://") {
			return errors.New("HTTPS token credentials require an HTTPS repository URL")
		}
	case AuthSSHKey:
		if !isSSH {
			return errors.New("SSH key credentials require a GitHub SSH repository URL")
		}
	default:
		return errors.New("unsupported repository credential type")
	}
	return nil
}

func (s *Service) repositoryPath(id string) (string, error) {
	if s.workspaceRoot == "" {
		return "", errors.New("Git repository workspace is not configured")
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", errors.New("invalid repository identifier")
	}
	root, err := filepath.Abs(s.workspaceRoot)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(root, id)
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("repository workspace escapes configured root")
	}
	return destination, nil
}

func (s *Service) openRepository(row Repository) (*gitclient.Repository, error) {
	path, err := s.repositoryPath(row.UUID)
	if err != nil {
		return nil, err
	}
	if err := s.validateExistingRepositoryPath(path); err != nil {
		return nil, err
	}
	repo, err := gitclient.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("open repository workspace: %w", err)
	}
	return repo, nil
}

func (s *Service) validateExistingRepositoryPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect repository workspace: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("repository workspace must be a real directory, not a symbolic link")
	}
	root, err := filepath.EvalSymlinks(s.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve repository workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve repository workspace: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("repository workspace resolves outside the configured root")
	}
	return nil
}

func (s *Service) repositoryView(row Repository) RepositoryView {
	present := false
	if path, err := s.repositoryPath(row.UUID); err == nil {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			present = true
		}
	}
	return RepositoryView{
		ID: row.UUID, Name: row.Name, Provider: row.Provider, RemoteURL: row.RemoteURL,
		DefaultBranch: row.DefaultBranch, Mode: row.Mode, CredentialID: row.CredentialUUID,
		Status: row.Status, LastError: row.LastError, LastFetchedAt: row.LastFetchedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, WorkspacePresent: present,
	}
}

func (s *Service) recordRepositoryError(row *Repository, err error) {
	row.Status, row.LastError = "error", safeGitError(err)
	_ = s.store.SaveRepository(row)
}

func safeGitError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	return message
}

func commitDistance(repo *gitclient.Repository, start, ancestor plumbing.Hash) int {
	if start == ancestor {
		return 0
	}
	type entry struct {
		hash     plumbing.Hash
		distance int
	}
	queue := []entry{{hash: start}}
	seen := map[plumbing.Hash]bool{}
	for len(queue) > 0 && len(seen) < 10000 {
		current := queue[0]
		queue = queue[1:]
		if seen[current.hash] {
			continue
		}
		seen[current.hash] = true
		if current.hash == ancestor {
			return current.distance
		}
		commit, err := repo.CommitObject(current.hash)
		if err != nil {
			continue
		}
		_ = commit.Parents().ForEach(func(parent *object.Commit) error {
			queue = append(queue, entry{hash: parent.Hash, distance: current.distance + 1})
			return nil
		})
	}
	return 0
}
