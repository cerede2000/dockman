package updater

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	registrycreds "github.com/RA341/dockman/internal/registry"
)

const registryManifestAccept = "application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json"

var bearerParameter = regexp.MustCompile(`([A-Za-z]+)="([^"]*)"`)

type registryImageReference struct {
	registry string
	repo     string
	tag      string
}

// SetRegistryCredentials wires the stored private-registry logins into the
// registry queries. It is set once at startup; without it, and for a registry
// nothing is stored for, every request stays anonymous and public
// repositories behave exactly as before.
func SetRegistryCredentials(lookup func(host string) (username, secret string, ok bool)) {
	credentialMu.Lock()
	defer credentialMu.Unlock()
	registryCredentialLookup = lookup
}

var (
	credentialMu             sync.RWMutex
	registryCredentialLookup func(host string) (string, string, bool)
)

type registryLogin struct {
	username string
	secret   string
	ok       bool
}

func lookupRegistryLogin(host string) registryLogin {
	credentialMu.RLock()
	lookup := registryCredentialLookup
	credentialMu.RUnlock()
	if lookup == nil {
		return registryLogin{}
	}
	username, secret, ok := lookup(host)
	return registryLogin{username: username, secret: secret, ok: ok}
}

func (l registryLogin) basic() string {
	return base64.StdEncoding.EncodeToString([]byte(l.username + ":" + l.secret))
}

// authorizeRegistry answers a registry challenge: Basic with the stored
// login, Bearer with a token obtained anonymously or with that login, which
// is what `docker login` does.
func authorizeRegistry(ctx context.Context, httpClient *http.Client, challenge, repo, host string) (string, error) {
	login := lookupRegistryLogin(host)
	scheme := strings.ToLower(strings.TrimSpace(challenge))
	switch {
	case strings.HasPrefix(scheme, "basic"):
		if !login.ok {
			return "", missingCredentialError(host)
		}
		return "Basic " + login.basic(), nil
	case strings.HasPrefix(scheme, "bearer"):
		token, err := bearerToken(ctx, httpClient, challenge, repo, login)
		if err != nil {
			return "", err
		}
		return "Bearer " + token, nil
	default:
		return "", fmt.Errorf("registry asked for an authentication scheme Dockman does not support (%q)", challenge)
	}
}

func missingCredentialError(host string) error {
	return fmt.Errorf("registry authentication required for %s; add a credential in Settings > Registries", host)
}

func rejectedCredentialError(host string, login registryLogin) error {
	if login.ok {
		return fmt.Errorf("%s rejected the stored credentials; check them in Settings > Registries", host)
	}
	return missingCredentialError(host)
}

// RegistryManifestDigest asks the registry directly. It deliberately does
// not use Docker's /distribution endpoint, so a read-only socket proxy needs
// no additional permission. Private repositories use the credential stored
// for their registry; without one the request stays anonymous.
func RegistryManifestDigest(ctx context.Context, image string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many registry redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("registry redirect must use HTTPS")
			}
			return nil
		},
	}
	return registryManifestDigestWithClient(ctx, image, client)
}

func registryManifestDigestWithClient(ctx context.Context, image string, httpClient *http.Client) (string, error) {
	ref, err := parseRegistryImageReference(image)
	if err != nil {
		return "", err
	}
	manifestURL := url.URL{
		Scheme: "https",
		Host:   ref.registry,
		Path:   "/v2/" + escapeRepositoryPath(ref.repo) + "/manifests/" + url.PathEscape(ref.tag),
	}

	digest, challenge, status, err := requestManifestDigest(ctx, httpClient, manifestURL.String(), "")
	if err != nil {
		return "", err
	}
	if status == http.StatusOK {
		return digest, nil
	}
	if status != http.StatusUnauthorized {
		return "", registryStatusError(status)
	}

	authorization, err := authorizeRegistry(ctx, httpClient, challenge, ref.repo, ref.registry)
	if err != nil {
		return "", err
	}
	digest, _, status, err = requestManifestDigest(ctx, httpClient, manifestURL.String(), authorization)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", rejectedCredentialError(ref.registry, lookupRegistryLogin(ref.registry))
	}
	if status != http.StatusOK {
		return "", registryStatusError(status)
	}
	return digest, nil
}

func requestManifestDigest(ctx context.Context, httpClient *http.Client, endpoint, authorization string) (string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Accept", registryManifestAccept)
	req.Header.Set("User-Agent", "Dockman/1.0")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	digest := strings.TrimPrefix(strings.TrimSpace(resp.Header.Get("Docker-Content-Digest")), "sha256:")
	if resp.StatusCode == http.StatusOK && digest == "" {
		return "", "", resp.StatusCode, errors.New("registry returned no Docker-Content-Digest header")
	}
	return digest, resp.Header.Get("WWW-Authenticate"), resp.StatusCode, nil
}

// bearerToken performs the token exchange. With a stored login it presents
// it as Basic authentication to the token endpoint; without, the request is
// anonymous, which is all a public repository needs.
func bearerToken(ctx context.Context, httpClient *http.Client, challenge, repo string, login registryLogin) (string, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(challenge)), "bearer ") {
		return "", errors.New("registry authentication required; only Basic and Bearer authentication are supported")
	}
	params := make(map[string]string)
	for _, match := range bearerParameter.FindAllStringSubmatch(challenge, -1) {
		params[strings.ToLower(match[1])] = match[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return "", errors.New("registry returned an invalid or insecure Bearer authentication realm")
	}
	query := realm.Query()
	if service := params["service"]; service != "" {
		query.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	query.Set("scope", scope)
	realm.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Dockman/1.0")
	if login.ok {
		req.Header.Set("Authorization", "Basic "+login.basic())
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", rejectedCredentialError(realm.Host, login)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token request returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode registry token: %w", err)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", errors.New("registry token response contained no token")
	}
	return payload.Token, nil
}

// parseRegistryImageReference splits a reference the way a registry request
// needs it. The rules live in the registry package, with the credential
// lookup that has to agree with them: a login stored for a host must be found
// for every image reference that resolves to that host.
func parseRegistryImageReference(image string) (registryImageReference, error) {
	ref, err := registrycreds.ParseImage(image)
	if err != nil {
		return registryImageReference{}, err
	}
	return registryImageReference{registry: ref.Registry, repo: ref.Repo, tag: ref.Tag}, nil
}

func escapeRepositoryPath(repo string) string {
	parts := strings.Split(repo, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func registryStatusError(status int) error {
	switch status {
	case http.StatusNotFound:
		return errors.New("image tag was not found in the public registry")
	case http.StatusTooManyRequests:
		return errors.New("public registry rate limit reached")
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("registry authentication required; add a credential in Settings > Registries")
	default:
		return fmt.Errorf("registry manifest request returned HTTP %d", status)
	}
}
