package updater

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// timeoutNetError is what a dial or read deadline looks like to the HTTP
// client: a net.Error whose Timeout reports true, with no context involved.
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func registryReply(status int, headers map[string]string, body string) *http.Response {
	response := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	for name, value := range headers {
		response.Header.Set(name, value)
	}
	return response
}

const bearerChallenge = `Bearer realm="https://auth.test/token",service="registry.test"`

// A registry check fails for reasons that wait fixes (a timeout, a cut
// connection, 429, 5xx) and reasons it does not (401, 403, 404, a bad
// reference). The class is what lets the scan replay the first kind and tell
// the operator at once about the second, so every case also pins the message
// the operator reads, which the class must never rewrite.
func TestRegistryFailuresAreSortedIntoTransientAndPermanent(t *testing.T) {
	SetRegistryCredentials(nil)
	manifest := func(status int, headers map[string]string) func(*http.Request) (*http.Response, error) {
		return func(*http.Request) (*http.Response, error) { return registryReply(status, headers, ""), nil }
	}
	failing := func(err error) func(*http.Request) (*http.Response, error) {
		return func(*http.Request) (*http.Response, error) { return nil, err }
	}
	// An anonymous Bearer exchange whose manifest request is answered 401 and
	// whose token request is handled by token.
	viaToken := func(token func(*http.Request) (*http.Response, error)) func(*http.Request) (*http.Response, error) {
		return func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/token" {
				return token(r)
			}
			return registryReply(http.StatusUnauthorized, map[string]string{"WWW-Authenticate": bearerChallenge}, ""), nil
		}
	}
	// Manifest answered 401 both times, with a token in between.
	rejectedAfterToken := func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/token" {
			return registryReply(http.StatusOK, nil, `{"token":"t"}`), nil
		}
		return registryReply(http.StatusUnauthorized, map[string]string{"WWW-Authenticate": bearerChallenge}, ""), nil
	}

	for _, test := range []struct {
		name    string
		image   string
		handler func(*http.Request) (*http.Response, error)
		want    RegistryErrorClass
		message string
	}{
		{"503 from the registry", "registry.test/team/app:latest", manifest(503, nil), RegistryErrorTransient, "registry manifest request returned HTTP 503"},
		{"500 from the registry", "registry.test/team/app:latest", manifest(500, nil), RegistryErrorTransient, "registry manifest request returned HTTP 500"},
		{"429 rate limit", "registry.test/team/app:latest", manifest(429, nil), RegistryErrorTransient, "public registry rate limit reached"},
		{"deadline of the HTTP client", "registry.test/team/app:latest", failing(context.DeadlineExceeded), RegistryErrorTransient, "context deadline exceeded"},
		{"network timeout", "registry.test/team/app:latest", failing(timeoutNetError{}), RegistryErrorTransient, "i/o timeout"},
		{"connection refused", "registry.test/team/app:latest", failing(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), RegistryErrorTransient, "connection refused"},
		{"connection cut", "registry.test/team/app:latest", failing(io.EOF), RegistryErrorTransient, "EOF"},
		{"token endpoint times out", "registry.test/team/app:latest", viaToken(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }), RegistryErrorTransient, "context deadline exceeded"},
		{"token endpoint 503", "registry.test/team/app:latest", viaToken(func(*http.Request) (*http.Response, error) { return registryReply(503, nil, ""), nil }), RegistryErrorTransient, "registry token request returned HTTP 503"},
		{"token body cut short", "registry.test/team/app:latest", viaToken(func(*http.Request) (*http.Response, error) { return registryReply(200, nil, `{"tok`), nil }), RegistryErrorTransient, "decode registry token"},

		{"404 tag not found", "registry.test/team/app:latest", manifest(404, nil), RegistryErrorNotFound, "image tag was not found in the public registry"},
		{"403 forbidden", "registry.test/team/app:latest", manifest(403, nil), RegistryErrorAuthentication, "registry authentication required; add a credential in Settings > Registries"},
		{"401 asking for Basic without a credential", "registry.test/team/app:latest", manifest(401, map[string]string{"WWW-Authenticate": `Basic realm="registry"`}), RegistryErrorAuthentication, "registry authentication required for registry.test; add a credential in Settings > Registries"},
		{"token endpoint refuses", "registry.test/team/app:latest", viaToken(func(*http.Request) (*http.Response, error) { return registryReply(401, nil, ""), nil }), RegistryErrorAuthentication, "registry authentication required for auth.test; add a credential in Settings > Registries"},
		{"401 again after a token", "registry.test/team/app:latest", rejectedAfterToken, RegistryErrorAuthentication, "registry authentication required for registry.test; add a credential in Settings > Registries"},

		{"400 is not transient", "registry.test/team/app:latest", manifest(400, nil), RegistryErrorOther, "registry manifest request returned HTTP 400"},
		{"cancelled by the caller", "registry.test/team/app:latest", failing(context.Canceled), RegistryErrorOther, "context canceled"},
		{"name that does not resolve", "registry.test/team/app:latest", failing(&net.DNSError{Err: "no such host", Name: "registry.test", IsNotFound: true}), RegistryErrorOther, "no such host"},
		{"invalid reference", "registry.test/team/app@sha256:abc", manifest(200, nil), RegistryErrorOther, "unsupported image reference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(test.handler)}
			_, err := registryManifestDigestWithClient(context.Background(), test.image, client)
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := classifyRegistryError(err); got != test.want {
				t.Errorf("class of %q = %q, want %q", err, got, test.want)
			}
			if !strings.Contains(err.Error(), test.message) {
				t.Errorf("message = %q, want it to contain %q", err, test.message)
			}
			if isTransient := errors.Is(err, errTransientRegistry); isTransient != (test.want == RegistryErrorTransient) {
				t.Errorf("errors.Is(transient) = %v for class %q", isTransient, test.want)
			}
		})
	}
}

// The classes keep the messages that are shown to the operator word for word.
func TestClassifiedRegistryErrorsKeepTheirMessage(t *testing.T) {
	for status, want := range map[int]string{
		404: "image tag was not found in the public registry",
		429: "public registry rate limit reached",
		401: "registry authentication required; add a credential in Settings > Registries",
		403: "registry authentication required; add a credential in Settings > Registries",
		502: "registry manifest request returned HTTP 502",
		418: "registry manifest request returned HTTP 418",
	} {
		if got := registryStatusError(status).Error(); got != want {
			t.Errorf("registryStatusError(%d) = %q, want %q", status, got, want)
		}
	}
	SetRegistryCredentials(func(string) (string, string, bool) { return "robot", "t0ken", true })
	t.Cleanup(func() { SetRegistryCredentials(nil) })
	rejected := rejectedCredentialError("registry.test", lookupRegistryLogin("registry.test"))
	if rejected.Error() != "registry.test rejected the stored credentials; check them in Settings > Registries" {
		t.Errorf("rejected credential message = %q", rejected)
	}
	if classifyRegistryError(rejected) != RegistryErrorAuthentication {
		t.Errorf("a rejected stored credential must be permanent, got %q", classifyRegistryError(rejected))
	}
}
