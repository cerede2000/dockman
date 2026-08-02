package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contSrv "github.com/RA341/dockman/internal/docker/container"
	hostMid "github.com/RA341/dockman/internal/host/middleware"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestExecPolicyRejectsDockmanBeforeWebSocketUpgrade(t *testing.T) {
	handler := newExecPolicyTestHandler(t, false)

	for _, path := range []string{"/exec/self-id/options", "/exec/self-id?cmd=/bin/sh"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req = req.WithContext(hostMid.SetHost(context.Background(), contSrv.LocalClient))
			res := httptest.NewRecorder()

			handler.ServeHTTP(res, req)

			require.Equal(t, http.StatusForbidden, res.Code)
			require.Contains(t, res.Body.String(), "DOCKMAN_ALLOW_SELF_EXEC=true")
		})
	}
}

func TestExecPolicyRejectsLocalHostShellBeforeWebSocketUpgrade(t *testing.T) {
	handler := newExecPolicyTestHandler(t, false)
	req := httptest.NewRequest(http.MethodGet, "/shell", nil)
	req = req.WithContext(hostMid.SetHost(context.Background(), contSrv.LocalClient))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	require.Equal(t, http.StatusForbidden, res.Code)
	require.Contains(t, res.Body.String(), "DOCKMAN_ALLOW_SELF_EXEC=true")
}

func TestHostShellOptionsReflectSelfExecPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		host          string
		allowSelfExec bool
		allowed       bool
	}{
		{name: "local disabled by default", host: contSrv.LocalClient, allowed: false},
		{name: "local explicitly enabled", host: contSrv.LocalClient, allowSelfExec: true, allowed: true},
		{name: "remote shell is not self exec", host: "ssh-host", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newExecPolicyTestHandler(t, tc.allowSelfExec)
			req := httptest.NewRequest(http.MethodGet, "/shell/options", nil)
			req = req.WithContext(hostMid.SetHost(context.Background(), tc.host))
			res := httptest.NewRecorder()

			handler.ServeHTTP(res, req)

			require.Equal(t, http.StatusOK, res.Code)
			if tc.allowed {
				require.JSONEq(t, `{"allowed":true}`, res.Body.String())
			} else {
				require.Contains(t, res.Body.String(), `"allowed":false`)
				require.Contains(t, res.Body.String(), "DOCKMAN_ALLOW_SELF_EXEC=true")
			}
		})
	}
}

func TestExecPolicyRejectsPrivilegedDebugBeforeWebSocketUpgrade(t *testing.T) {
	handler := newExecPolicyTestHandler(t, false)
	req := httptest.NewRequest(http.MethodGet, "/exec/other-id?debug=1&image=example/debug", nil)
	req = req.WithContext(hostMid.SetHost(context.Background(), contSrv.LocalClient))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	require.Equal(t, http.StatusForbidden, res.Code)
	require.Contains(t, res.Body.String(), "privileged debug containers are disabled")
}

func TestExecPolicyCanBeExplicitlyEnabled(t *testing.T) {
	handler := newExecPolicyTestHandler(t, true)
	req := httptest.NewRequest(http.MethodGet, "/exec/self-id/options", nil)
	req = req.WithContext(hostMid.SetHost(context.Background(), contSrv.LocalClient))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	require.Equal(t, http.StatusOK, res.Code)
	require.JSONEq(t, `{"shells":[]}`, res.Body.String())
}

func newExecPolicyTestHandler(t *testing.T, allowSelfExec bool) http.Handler {
	t.Helper()
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("not found")),
			Request:    r,
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/self-id/json") {
			response.StatusCode = http.StatusOK
			response.Header.Set("Content-Type", "application/json")
			response.Body = io.NopCloser(strings.NewReader(`{"Id":"self-id","Config":{"Labels":{"dockman.container":"true"}}}`))
		}
		return response, nil
	})}

	cli, err := client.New(
		client.WithHost("http://docker-proxy"),
		client.WithHTTPClient(httpClient),
		client.WithVersion("1.52"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })

	dkSrv := &Service{Container: contSrv.New(cli)}
	return NewHandlerHttp(func(host string) (*Service, error) {
		require.Equal(t, contSrv.LocalClient, host)
		return dkSrv, nil
	}, allowSelfExec)
}
