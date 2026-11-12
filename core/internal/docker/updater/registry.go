package updater

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

// registryRequestTimeout bounds each single request to a registry. An image
// check is up to three of them in series, see registryCheckBudget in scan.go.
const registryRequestTimeout = 15 * time.Second

// Registry errors are sorted into the question an operator asks first: will
// waiting fix it. The sentinels below travel inside the error with
// errors.Is and never change its message, which is shown to the user and
// asserted by tests.
//
// Transient: a timeout, a refused or cut connection, a resolver that fails or
// does not answer, HTTP 408, 429 and 5xx. The same request is expected to work
// a moment later.
//
// Permanent: 401 and 403 (authentication), 404 (not found), an invalid
// reference, a rejected credential. Repeating the request cannot help; the
// operator has to act.
var (
	errTransientRegistry = errors.New("transient registry failure")
	errRegistryAuth      = errors.New("registry authentication failure")
	errRegistryNotFound  = errors.New("registry reference not found")
)

// RegistryErrorClass is the part of a registry failure that survives a change
// of wording. Notification fingerprints are built on it, never on the raw text:
// a timeout reads differently depending on which layer noticed it first.
type RegistryErrorClass string

const (
	RegistryErrorTransient      RegistryErrorClass = "transient"
	RegistryErrorAuthentication RegistryErrorClass = "authentication"
	RegistryErrorNotFound       RegistryErrorClass = "not-found"
	RegistryErrorOther          RegistryErrorClass = "other"
)

// classifiedRegistryError keeps the original message word for word and
// attaches the sentinel it belongs to.
type classifiedRegistryError struct {
	err   error
	class error
}

func (e *classifiedRegistryError) Error() string   { return e.err.Error() }
func (e *classifiedRegistryError) Unwrap() []error { return []error{e.err, e.class} }

func classified(err, class error) error {
	if err == nil {
		return nil
	}
	return &classifiedRegistryError{err: err, class: class}
}

// classifyRegistryError reads the sentinel back. Anything unmarked is "other",
// which callers treat as permanent: an unknown failure is shown at once rather
// than hidden behind a replay.
func classifyRegistryError(err error) RegistryErrorClass {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errTransientRegistry):
		return RegistryErrorTransient
	case errors.Is(err, errRegistryAuth):
		return RegistryErrorAuthentication
	case errors.Is(err, errRegistryNotFound):
		return RegistryErrorNotFound
	default:
		return RegistryErrorOther
	}
}

// transportError marks a failure of the HTTP exchange itself. A cancellation
// is the caller's decision and is never transient; a name that does not
// resolve is a configuration mistake, not a hiccup.
func transportError(err error) error {
	if err == nil || !isTransientTransportError(err) {
		return err
	}
	return classified(err, errTransientRegistry)
}

func isTransientTransportError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// dial refused, connection reset, broken pipe, TLS alert: the other end
		// or the path to it, not the request.
		//
		// This is deliberately broad: it also catches a durable TLS alert from
		// the peer ("remote error: tls: handshake failure"), which is then taken
		// for a transient failure, replayed once, and notified one scan later
		// than a permanent error would be. Telling a lasting alert from a passing
		// one is not something the error allows, and a delay of one scan is the
		// cheaper mistake.
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

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
	return classified(fmt.Errorf("registry authentication required for %s; add a credential in Settings > Registries", host), errRegistryAuth)
}

func rejectedCredentialError(host string, login registryLogin) error {
	if login.ok {
		return classified(fmt.Errorf("%s rejected the stored credentials; check them in Settings > Registries", host), errRegistryAuth)
	}
	return missingCredentialError(host)
}

// RegistryManifestDigest asks the registry directly. It deliberately does
// not use Docker's /distribution endpoint, so a read-only socket proxy needs
// no additional permission. Private repositories use the credential stored
// for their registry; without one the request stays anonymous.
func RegistryManifestDigest(ctx context.Context, image string) (string, error) {
	client := &http.Client{
		Timeout: registryRequestTimeout,
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
		return "", "", 0, transportError(err)
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
		return "", transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", rejectedCredentialError(realm.Host, login)
	}
	if resp.StatusCode != http.StatusOK {
		tokenErr := fmt.Errorf("registry token request returned HTTP %d", resp.StatusCode)
		if transientStatus(resp.StatusCode) {
			return "", classified(tokenErr, errTransientRegistry)
		}
		return "", tokenErr
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&payload); err != nil {
		// A body cut short by the same timeout or reset is transient too.
		return "", transportError(fmt.Errorf("decode registry token: %w", err))
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

// transientStatus is the HTTP half of the transient class: the request timed
// out on the way (408), asked to slow down (429), or the registry (or what
// stands in front of it) failed (5xx).
func transientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func registryStatusError(status int) error {
	switch status {
	case http.StatusNotFound:
		return classified(errors.New("image tag was not found in the public registry"), errRegistryNotFound)
	case http.StatusTooManyRequests:
		return classified(errors.New("public registry rate limit reached"), errTransientRegistry)
	case http.StatusUnauthorized, http.StatusForbidden:
		return classified(errors.New("registry authentication required; add a credential in Settings > Registries"), errRegistryAuth)
	default:
		err := fmt.Errorf("registry manifest request returned HTTP %d", status)
		if transientStatus(status) {
			return classified(err, errTransientRegistry)
		}
		return err
	}
}
