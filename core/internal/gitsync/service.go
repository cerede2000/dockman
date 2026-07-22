package gitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type secretPayload struct {
	Token      string `json:"token,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

type CredentialInput struct {
	Name       string `json:"name"`
	AuthType   string `json:"authType"`
	Username   string `json:"username"`
	Token      string `json:"token"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

type CredentialView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	AuthType   string    `json:"authType"`
	Username   string    `json:"username,omitempty"`
	HasSecret  bool      `json:"hasSecret"`
	SecretHint string    `json:"secretHint,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type TestInput struct {
	CredentialInput
	RepositoryURL string `json:"repositoryUrl"`
}

type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Service struct {
	enabled       bool
	store         *Store
	vault         *Vault
	http          *http.Client
	githubAPIBase string
	workspaceRoot string
	locksMu       sync.Mutex
	locks         map[string]*sync.Mutex
}

func NewService(enabled bool, store *Store, vault *Vault, workspaceRoot ...string) *Service {
	root := ""
	if len(workspaceRoot) > 0 {
		root = workspaceRoot[0]
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("GitHub API redirect refused")
		},
	}
	return &Service{enabled: enabled, store: store, vault: vault, http: client, githubAPIBase: "https://api.github.com", workspaceRoot: root, locks: map[string]*sync.Mutex{}}
}

func (s *Service) Enabled() bool { return s.enabled }

func (s *Service) RecoverInterruptedOperations() (int64, error) {
	if !s.enabled {
		return 0, nil
	}
	return s.store.MarkInterruptedOperations()
}

func (s *Service) RunRepositoryOperation(ctx context.Context, repositoryID, operationType string, fn func(context.Context) error) error {
	if !s.enabled {
		return errors.New("Git synchronization is disabled")
	}
	now := time.Now().UTC()
	op := &Operation{UUID: uuid.NewString(), RepositoryUUID: repositoryID, OperationType: operationType, State: "running", StartedAt: &now}
	if err := s.store.StartOperation(op); err != nil {
		return err
	}
	err := fn(ctx)
	state, message := "success", ""
	if err != nil {
		state, message = "failed", safeGitError(err)
	}
	if finishErr := s.store.FinishOperation(op.UUID, state, message); finishErr != nil && err == nil {
		return finishErr
	}
	return err
}

func (s *Service) repositoryLock(id string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if s.locks[id] == nil {
		s.locks[id] = &sync.Mutex{}
	}
	return s.locks[id]
}

func (s *Service) ListCredentials() ([]CredentialView, error) {
	rows, err := s.store.ListCredentials()
	if err != nil {
		return nil, err
	}
	out := make([]CredentialView, 0, len(rows))
	for _, row := range rows {
		out = append(out, credentialView(row))
	}
	return out, nil
}

func (s *Service) CreateCredential(input CredentialInput) (CredentialView, error) {
	if !s.enabled {
		return CredentialView{}, errors.New("Git synchronization is disabled")
	}
	input, payload, err := validateCredentialInput(input, false)
	if err != nil {
		return CredentialView{}, err
	}
	id := uuid.NewString()
	encrypted, err := s.encryptPayload(id, payload)
	if err != nil {
		return CredentialView{}, err
	}
	row := Credential{UUID: id, Name: input.Name, AuthType: input.AuthType, Username: input.Username, EncryptedPayload: encrypted, SecretHint: secretHint(payload)}
	if err := s.store.SaveCredential(&row); err != nil {
		return CredentialView{}, err
	}
	return credentialView(row), nil
}

func (s *Service) UpdateCredential(id string, input CredentialInput) (CredentialView, error) {
	row, err := s.store.GetCredential(id)
	if err != nil {
		return CredentialView{}, err
	}
	input, payload, err := validateCredentialInput(input, true)
	if err != nil {
		return CredentialView{}, err
	}
	if row.AuthType != input.AuthType && input.AuthType != AuthPublic && payload.Token == "" && payload.PrivateKey == "" {
		return CredentialView{}, errors.New("a new secret is required when changing credential type")
	}
	row.Name, row.AuthType, row.Username = input.Name, input.AuthType, input.Username
	if payload.Token != "" || payload.PrivateKey != "" || input.AuthType == AuthPublic {
		row.EncryptedPayload, err = s.encryptPayload(id, payload)
		if err != nil {
			return CredentialView{}, err
		}
		row.SecretHint = secretHint(payload)
	}
	if err := s.store.SaveCredential(&row); err != nil {
		return CredentialView{}, err
	}
	return credentialView(row), nil
}

func (s *Service) DeleteCredential(id string) error {
	inUse, err := s.store.CredentialInUse(id)
	if err != nil {
		return err
	}
	if inUse {
		return errors.New("credential is still used by a repository")
	}
	return s.store.DeleteCredential(id)
}

func (s *Service) TestSavedCredential(ctx context.Context, id, repositoryURL string) (TestResult, error) {
	row, err := s.store.GetCredential(id)
	if err != nil {
		return TestResult{}, err
	}
	payload, err := s.decryptPayload(row)
	if err != nil {
		return TestResult{}, err
	}
	return s.testCredential(ctx, CredentialInput{Name: row.Name, AuthType: row.AuthType, Username: row.Username, Token: payload.Token, PrivateKey: payload.PrivateKey, Passphrase: payload.Passphrase}, repositoryURL)
}

func (s *Service) TestCredential(ctx context.Context, input TestInput) (TestResult, error) {
	clean, _, err := validateCredentialInput(input.CredentialInput, false)
	if err != nil {
		return TestResult{}, err
	}
	return s.testCredential(ctx, clean, input.RepositoryURL)
}

func (s *Service) testCredential(ctx context.Context, input CredentialInput, repositoryURL string) (TestResult, error) {
	switch input.AuthType {
	case AuthPublic:
		if repositoryURL == "" {
			return TestResult{OK: true, Message: "Public access needs no credential. Add a repository URL to verify remote access."}, nil
		}
		if err := validateGitHubURL(repositoryURL, false); err != nil {
			return TestResult{}, err
		}
		if err := listRemote(ctx, repositoryURL, nil); err != nil {
			return TestResult{}, fmt.Errorf("public repository access failed: %w", err)
		}
		return TestResult{OK: true, Message: "Public repository is reachable."}, nil
	case AuthHTTPSToken:
		if repositoryURL != "" {
			if err := validateGitHubURL(repositoryURL, false); err != nil {
				return TestResult{}, err
			}
			username := input.Username
			if username == "" {
				username = "x-access-token"
			}
			if err := listRemote(ctx, repositoryURL, &githttp.BasicAuth{Username: username, Password: input.Token}); err != nil {
				return TestResult{}, fmt.Errorf("GitHub repository access failed: %w", err)
			}
			return TestResult{OK: true, Message: "GitHub repository is reachable with this token."}, nil
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPIBase+"/user", nil)
		req.Header.Set("Authorization", "Bearer "+input.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
		resp, err := s.http.Do(req)
		if err != nil {
			return TestResult{}, fmt.Errorf("GitHub connection failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return TestResult{}, fmt.Errorf("GitHub rejected the token (HTTP %d)", resp.StatusCode)
		}
		return TestResult{OK: true, Message: "GitHub accepted the token."}, nil
	case AuthSSHKey:
		if repositoryURL != "" {
			if err := validateGitHubURL(repositoryURL, true); err != nil {
				return TestResult{}, err
			}
		}
		key, err := gitssh.NewPublicKeys("git", []byte(input.PrivateKey), input.Passphrase)
		if err != nil {
			return TestResult{}, fmt.Errorf("invalid SSH private key or passphrase: %w", err)
		}
		if key.Signer == nil {
			return TestResult{}, errors.New("invalid SSH private key")
		}
		if repositoryURL == "" {
			return TestResult{OK: true, Message: "SSH private key and passphrase are valid. Add a repository URL to verify remote access."}, nil
		}
		hostKeys, err := s.githubHostKeys(ctx)
		if err != nil {
			return TestResult{}, err
		}
		key.HostKeyCallback = trustedHostKeyCallback(hostKeys)
		if err := listRemote(ctx, repositoryURL, key); err != nil {
			return TestResult{}, fmt.Errorf("GitHub SSH repository access failed: %w", err)
		}
		return TestResult{OK: true, Message: "GitHub repository is reachable with this SSH key."}, nil
	default:
		return TestResult{}, errors.New("unsupported credential type")
	}
}

func (s *Service) githubHostKeys(ctx context.Context) ([]ssh.PublicKey, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPIBase+"/meta", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("retrieve GitHub SSH host keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("retrieve GitHub SSH host keys: HTTP %d", resp.StatusCode)
	}
	var metadata struct {
		SSHKeys []string `json:"ssh_keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode GitHub SSH host keys: %w", err)
	}
	keys := make([]ssh.PublicKey, 0, len(metadata.SSHKeys))
	for _, encoded := range metadata.SSHKeys {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(encoded))
		if err == nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("GitHub API returned no usable SSH host key")
	}
	return keys, nil
}

func trustedHostKeyCallback(hostKeys []ssh.PublicKey) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, remoteKey ssh.PublicKey) error {
		for _, trusted := range hostKeys {
			if trusted.Type() == remoteKey.Type() && bytes.Equal(trusted.Marshal(), remoteKey.Marshal()) {
				return nil
			}
		}
		return errors.New("GitHub SSH host key does not match keys published by the GitHub API")
	}
}

func listRemote(ctx context.Context, remoteURL string, auth transport.AuthMethod) error {
	remote := gitclient.NewRemote(nil, &config.RemoteConfig{Name: "origin", URLs: []string{remoteURL}})
	opts := &gitclient.ListOptions{Auth: auth}
	_, err := remote.ListContext(ctx, opts)
	return err
}

func validateCredentialInput(input CredentialInput, allowEmptySecret bool) (CredentialInput, secretPayload, error) {
	input.Name, input.Username = strings.TrimSpace(input.Name), strings.TrimSpace(input.Username)
	if input.Name == "" || len(input.Name) > 100 {
		return input, secretPayload{}, errors.New("credential name is required and must be at most 100 characters")
	}
	payload := secretPayload{Token: strings.TrimSpace(input.Token), PrivateKey: strings.TrimSpace(input.PrivateKey), Passphrase: input.Passphrase}
	switch input.AuthType {
	case AuthPublic:
		input.Username, payload = "", secretPayload{}
	case AuthHTTPSToken:
		if payload.Token == "" && !allowEmptySecret {
			return input, payload, errors.New("GitHub token is required")
		}
		input.PrivateKey, input.Passphrase = "", ""
	case AuthSSHKey:
		if payload.PrivateKey == "" && !allowEmptySecret {
			return input, payload, errors.New("SSH private key is required")
		}
		input.Username = "git"
		input.Token = ""
	default:
		return input, payload, errors.New("authType must be public, https_token, or ssh_key")
	}
	return input, payload, nil
}

func validateGitHubURL(raw string, allowSSH bool) error {
	if strings.HasPrefix(raw, "git@github.com:") {
		if allowSSH && githubRepositoryPathPattern.MatchString(strings.TrimPrefix(raw, "git@github.com:")) {
			return nil
		}
		return errors.New("SSH repository URL is not allowed for this credential type")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("repository URL must be a credential-free github.com URL")
	}
	if u.Scheme == "https" && u.User == nil && u.Port() == "" && githubRepositoryPathPattern.MatchString(strings.TrimPrefix(u.Path, "/")) {
		return nil
	}
	if allowSSH && u.Scheme == "ssh" && u.User != nil && u.User.String() == "git" && (u.Port() == "" || u.Port() == "22") && githubRepositoryPathPattern.MatchString(strings.TrimPrefix(u.Path, "/")) {
		return nil
	}
	return errors.New("repository URL must end in .git and use HTTPS, or GitHub SSH where supported")
}

func (s *Service) encryptPayload(id string, payload secretPayload) ([]byte, error) {
	if payload == (secretPayload{}) {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return s.vault.Encrypt(raw, id)
}

func (s *Service) decryptPayload(row Credential) (secretPayload, error) {
	if len(row.EncryptedPayload) == 0 {
		return secretPayload{}, nil
	}
	raw, err := s.vault.Decrypt(row.EncryptedPayload, row.UUID)
	if err != nil {
		return secretPayload{}, err
	}
	var payload secretPayload
	err = json.Unmarshal(raw, &payload)
	return payload, err
}

func credentialView(row Credential) CredentialView {
	return CredentialView{ID: row.UUID, Name: row.Name, AuthType: row.AuthType, Username: row.Username, HasSecret: len(row.EncryptedPayload) > 0, SecretHint: row.SecretHint, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func secretHint(payload secretPayload) string {
	if payload.Token != "" {
		return "GitHub token configured"
	}
	if payload.PrivateKey != "" {
		if signer, err := ssh.ParsePrivateKey([]byte(payload.PrivateKey)); err == nil {
			return ssh.FingerprintSHA256(signer.PublicKey())
		}
		return "SSH key"
	}
	return ""
}
