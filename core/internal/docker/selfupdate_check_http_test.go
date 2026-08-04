package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contSrv "github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/internal/docker/updater"
	hostMid "github.com/RA341/dockman/internal/host/middleware"
	apiContainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestCheckDockmanUpdateReportsLocalImageWithoutUpdating(t *testing.T) {
	requests := make([]string, 0, 2)
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			var body strings.Builder
			require.NoError(t, json.NewEncoder(&body).Encode([]apiContainer.Summary{{
				ID: "self-id", Image: "dockman:test", ImageID: "sha256:local",
				Labels: map[string]string{dockmanContainerLabel: "true"},
			}}))
			response.Header.Set("Content-Type", "application/json")
			response.Body = io.NopCloser(strings.NewReader(body.String()))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/sha256:local/json"):
			response.Header.Set("Content-Type", "application/json")
			response.Body = io.NopCloser(strings.NewReader(`{"Id":"sha256:local","RepoDigests":[]}`))
		default:
			response.StatusCode = http.StatusNotFound
			response.Body = io.NopCloser(strings.NewReader("unexpected daemon request"))
		}
		return response, nil
	})}

	cli, err := client.New(client.WithHost("http://docker-proxy"), client.WithHTTPClient(httpClient), client.WithVersion("1.52"))
	require.NoError(t, err)
	defer cli.Close()

	containers := contSrv.New(cli)
	dkSrv := &Service{Container: containers, Updater: updater.New(containers, "local", "", nil)}
	handler := NewHandlerHttp(func(host string) (*Service, error) {
		require.Equal(t, contSrv.LocalClient, host)
		return dkSrv, nil
	}, false)

	req := httptest.NewRequest(http.MethodGet, "/update/dockman/check", nil)
	req = req.WithContext(hostMid.SetHost(context.Background(), contSrv.LocalClient))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	require.Equal(t, http.StatusOK, res.Code)
	var result updater.ContainerUpdateCheck
	require.NoError(t, json.NewDecoder(res.Body).Decode(&result))
	require.Equal(t, updater.ContainerUpdateSkipped, result.Status)
	require.Contains(t, result.Reason, "locally built image")
	require.Equal(t, []string{
		"GET /v1.52/containers/json",
		"GET /v1.52/images/sha256:local/json",
	}, requests, "the read-only check must not pull or recreate anything")
}

func TestCheckDockmanUpdateRejectsRemoteHost(t *testing.T) {
	handler := NewHandlerHttp(func(string) (*Service, error) {
		t.Fatal("service provider must not be called for a remote host")
		return nil, nil
	}, false)
	req := httptest.NewRequest(http.MethodGet, "/update/dockman/check", nil)
	req = req.WithContext(hostMid.SetHost(context.Background(), "remote"))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	require.Equal(t, http.StatusBadRequest, res.Code)
}
