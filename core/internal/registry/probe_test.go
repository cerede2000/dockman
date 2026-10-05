package registry

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Test button has one job: say whether this login would be accepted,
// before a deployment finds out instead. So it performs the exchange a
// registry really asks for.
func bearerRegistry(t *testing.T, accept string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/token"):
			if r.Header.Get("Authorization") != accept {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"granted"}`))
		case r.URL.Path == "/v2/":
			if r.Header.Get("Authorization") != "Bearer granted" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="private"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return server
}

func hostOf(server *httptest.Server) string { return strings.TrimPrefix(server.URL, "https://") }

func TestATestedLoginIsAcceptedByTheRegistry(t *testing.T) {
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:t0ken"))
	server := bearerRegistry(t, expected)
	defer server.Close()

	err := probeWithClient(context.Background(),
		Auth{Registry: hostOf(server), Username: "robot", Secret: "t0ken"}, server.Client())
	require.NoError(t, err)
}

func TestAWrongSecretIsReportedAsRefused(t *testing.T) {
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:t0ken"))
	server := bearerRegistry(t, expected)
	defer server.Close()

	err := probeWithClient(context.Background(),
		Auth{Registry: hostOf(server), Username: "robot", Secret: "wrong"}, server.Client())
	require.Error(t, err)
	require.Contains(t, err.Error(), "rejected these credentials")
}

// Registries that ask for Basic authentication directly - a plain registry:2
// behind htpasswd, the usual self-hosted one - are supported too.
func TestABasicRegistryIsSupported(t *testing.T) {
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:t0ken"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, probeWithClient(context.Background(),
		Auth{Registry: hostOf(server), Username: "robot", Secret: "t0ken"}, server.Client()))
	err := probeWithClient(context.Background(),
		Auth{Registry: hostOf(server), Username: "robot", Secret: "nope"}, server.Client())
	require.Error(t, err)
	require.Contains(t, err.Error(), "rejected these credentials")
}

// A registry that answers without asking for anything proves nothing about
// the credentials, and saying "ok" there would be a lie.
func TestAnOpenRegistrySaysTheCredentialsWereNotChecked(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := probeWithClient(context.Background(),
		Auth{Registry: hostOf(server), Username: "robot", Secret: "t0ken"}, server.Client())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not checked")
}

func TestAnUnreachableRegistryIsNamed(t *testing.T) {
	err := probeWithClient(context.Background(),
		Auth{Registry: "127.0.0.1:1", Username: "robot", Secret: "t0ken"}, defaultClient(2_000_000_000))
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not reach 127.0.0.1:1")
}
