package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
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

// RegistryManifestDigest asks the registry directly. It deliberately does
// not use Docker's /distribution endpoint, so a read-only socket proxy needs
// no additional permission. This lot supports public repositories only;
// Bearer tokens are requested anonymously and credentials are never read.
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

	token, err := anonymousBearerToken(ctx, httpClient, challenge, ref.repo)
	if err != nil {
		return "", err
	}
	digest, _, status, err = requestManifestDigest(ctx, httpClient, manifestURL.String(), "Bearer "+token)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", errors.New("registry authentication required; private registry credentials are not configured")
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

func anonymousBearerToken(ctx context.Context, httpClient *http.Client, challenge, repo string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(challenge)), "bearer ") {
		return "", errors.New("registry authentication required; only anonymous Bearer authentication is supported")
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
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", errors.New("registry authentication required; private registry credentials are not configured")
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

func parseRegistryImageReference(image string) (registryImageReference, error) {
	image = strings.TrimSpace(image)
	if image == "" || strings.Contains(image, "://") || strings.Contains(image, "@") {
		return registryImageReference{}, fmt.Errorf("unsupported image reference %q", image)
	}
	registry := "registry-1.docker.io"
	remainder := image
	if slash := strings.IndexByte(image, '/'); slash >= 0 {
		first := image[:slash]
		if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
			registry = first
			remainder = image[slash+1:]
		}
	}
	if registry == "docker.io" || registry == "index.docker.io" {
		registry = "registry-1.docker.io"
	}
	lastSlash := strings.LastIndexByte(remainder, '/')
	lastColon := strings.LastIndexByte(remainder, ':')
	tag := "latest"
	if lastColon > lastSlash {
		tag = remainder[lastColon+1:]
		remainder = remainder[:lastColon]
	}
	if registry == "registry-1.docker.io" && !strings.Contains(remainder, "/") {
		remainder = "library/" + remainder
	}
	if registry == "" || remainder == "" || tag == "" {
		return registryImageReference{}, fmt.Errorf("invalid image reference %q", image)
	}
	return registryImageReference{registry: registry, repo: remainder, tag: tag}, nil
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
		return errors.New("registry authentication required; private registry credentials are not configured")
	default:
		return fmt.Errorf("registry manifest request returned HTTP %d", status)
	}
}
