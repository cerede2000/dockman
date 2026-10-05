package registry

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
	"time"
)

var bearerParameter = regexp.MustCompile(`([A-Za-z]+)="([^"]*)"`)

// Probe answers the only question the Test button asks: would this login be
// accepted? It performs the real exchange - the registry's own
// /v2/ challenge, then a token obtained with these credentials - instead of
// checking the form, so a typo in a token is reported here rather than in the
// middle of a deployment.
func Probe(ctx context.Context, auth Auth) error {
	return probeWithClient(ctx, auth, defaultClient(15*time.Second))
}

func defaultClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
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
}

func probeWithClient(ctx context.Context, auth Auth, httpClient *http.Client) error {
	host := NormalizeHost(auth.Registry)
	if host == "" {
		return errors.New("a registry host is required")
	}
	endpoint := (&url.URL{Scheme: "https", Host: host, Path: "/v2/"}).String()

	status, challenge, err := probeRequest(ctx, httpClient, endpoint, "")
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", host, err)
	}
	switch {
	case status == http.StatusOK:
		// An open registry: the credentials are not exercised, so say so
		// rather than report a success they had no part in.
		return fmt.Errorf("%s answered without asking for authentication; the credentials were not checked", host)
	case status != http.StatusUnauthorized:
		return fmt.Errorf("%s answered HTTP %d to the authentication probe", host, status)
	}

	scheme := strings.ToLower(strings.Fields(strings.TrimSpace(challenge) + " ")[0])
	switch scheme {
	case "basic":
		status, _, err = probeRequest(ctx, httpClient, endpoint, "Basic "+basic(auth))
	case "bearer":
		token, tokenErr := bearerToken(ctx, httpClient, challenge, "", auth, true)
		if tokenErr != nil {
			return tokenErr
		}
		status, _, err = probeRequest(ctx, httpClient, endpoint, "Bearer "+token)
	default:
		return fmt.Errorf("%s asked for an authentication scheme Dockman does not support (%q)", host, challenge)
	}
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", host, err)
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s rejected these credentials", host)
	default:
		return fmt.Errorf("%s answered HTTP %d once authenticated", host, status)
	}
}

func probeRequest(ctx context.Context, httpClient *http.Client, endpoint, authorization string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Dockman/1.0")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
}

// bearerToken runs the token exchange a registry asks for. With credentials
// it sends them as Basic authentication to the token endpoint, which is what
// `docker login` does; without, the request stays anonymous and public
// repositories keep working exactly as before.
func bearerToken(ctx context.Context, httpClient *http.Client, challenge, repo string, auth Auth, hasAuth bool) (string, error) {
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
	if scope == "" && repo != "" {
		scope = "repository:" + repo + ":pull"
	}
	if scope != "" {
		query.Set("scope", scope)
	}
	realm.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Dockman/1.0")
	if hasAuth {
		req.Header.Set("Authorization", "Basic "+basic(auth))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if hasAuth {
			return "", fmt.Errorf("%s rejected these credentials", NormalizeHost(auth.Registry))
		}
		return "", errors.New("registry authentication required; add a credential in Settings > Registries")
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

func basic(auth Auth) string {
	return base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Secret))
}
