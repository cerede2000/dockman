package updater

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestParseRegistryImageReference(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		image               string
		registry, repo, tag string
	}{
		{"alpine", "registry-1.docker.io", "library/alpine", "latest"},
		{"library/nginx:stable", "registry-1.docker.io", "library/nginx", "stable"},
		{"ghcr.io/example/app:v1", "ghcr.io", "example/app", "v1"},
		{"registry.example:5000/team/app", "registry.example:5000", "team/app", "latest"},
	} {
		got, err := parseRegistryImageReference(test.image)
		if err != nil {
			t.Fatalf("parse %q: %v", test.image, err)
		}
		if got.registry != test.registry || got.repo != test.repo || got.tag != test.tag {
			t.Errorf("parse %q = %#v", test.image, got)
		}
	}
}

func TestRegistryManifestDigestAnonymousBearer(t *testing.T) {
	t.Parallel()
	const digest = "0123456789abcdef"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:team/app:pull" {
				t.Errorf("unexpected scope %q", r.URL.Query().Get("scope"))
			}
			response.Body = io.NopCloser(strings.NewReader(`{"token":"public-token"}`))
		case "/v2/team/app/manifests/latest":
			if r.Header.Get("Authorization") != "Bearer public-token" {
				response.StatusCode = http.StatusUnauthorized
				response.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.test/token",service="test"`)
				return response, nil
			}
			response.Header.Set("Docker-Content-Digest", "sha256:"+digest)
		default:
			response.StatusCode = http.StatusNotFound
		}
		return response, nil
	})}

	got, err := registryManifestDigestWithClient(context.Background(), "registry.test/team/app:latest", client)
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
}

// A registry that asks for Basic authentication used to be refused outright.
// It is now answered with the stored login, and the refusal, when there is
// none, says where to add one instead of describing what Dockman supports.
func TestABasicChallengeWithoutACredentialSaysWhereToAddOne(t *testing.T) {
	SetRegistryCredentials(nil)
	_, err := authorizeRegistry(context.Background(), http.DefaultClient, `Basic realm="registry"`, "team/app", "registry.test")
	if err == nil || !strings.Contains(err.Error(), "Settings > Registries") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "registry.test") {
		t.Fatalf("the error must name the registry: %v", err)
	}
}

func TestABasicChallengeUsesTheStoredCredential(t *testing.T) {
	SetRegistryCredentials(func(host string) (string, string, bool) {
		if host != "registry.test" {
			return "", "", false
		}
		return "robot", "t0ken", true
	})
	t.Cleanup(func() { SetRegistryCredentials(nil) })

	authorization, err := authorizeRegistry(context.Background(), http.DefaultClient, `Basic realm="registry"`, "team/app", "registry.test")
	if err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:t0ken"))
	if authorization != want {
		t.Fatalf("authorization = %q, want %q", authorization, want)
	}
}

// The token endpoint of a private registry only answers to a login, and that
// is where `docker login` presents it too.
func TestABearerTokenIsRequestedWithTheStoredCredential(t *testing.T) {
	var seen string
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		if seen == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"token":"granted"}`))
	}))
	t.Cleanup(tokenServer.Close)

	SetRegistryCredentials(func(string) (string, string, bool) { return "robot", "t0ken", true })
	t.Cleanup(func() { SetRegistryCredentials(nil) })

	client := tokenServer.Client()
	challenge := `Bearer realm="` + tokenServer.URL + `",service="registry.test"`
	authorization, err := authorizeRegistry(context.Background(), client, challenge, "team/app", "registry.test")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer granted" {
		t.Fatalf("authorization = %q", authorization)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:t0ken")); seen != want {
		t.Fatalf("token request carried %q, want %q", seen, want)
	}
}

// Without a stored login the exchange stays anonymous: public repositories
// must keep working exactly as they did.
func TestABearerTokenStaysAnonymousWithoutACredential(t *testing.T) {
	var seen string
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"token":"public"}`))
	}))
	t.Cleanup(tokenServer.Close)
	SetRegistryCredentials(nil)

	challenge := `Bearer realm="` + tokenServer.URL + `",service="registry.test"`
	authorization, err := authorizeRegistry(context.Background(), tokenServer.Client(), challenge, "team/app", "registry.test")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer public" || seen != "" {
		t.Fatalf("authorization = %q, token request carried %q", authorization, seen)
	}
}
