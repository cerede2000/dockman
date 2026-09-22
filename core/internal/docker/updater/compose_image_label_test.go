package updater

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func manifest(id, arch string, available bool) image.ManifestSummary {
	data := &image.ImageProperties{}
	data.Platform.OS, data.Platform.Architecture = "linux", arch
	return image.ManifestSummary{ID: id, Kind: image.ManifestKindImage, Available: available, ImageData: data}
}

func attestation(id string) image.ManifestSummary {
	return image.ManifestSummary{ID: id, Kind: image.ManifestKindAttestation, Available: true}
}

// index is a pulled multi-platform image on the containerd store: its ID is
// the index, and only the host's manifest is available.
func index(id, hostManifest string) image.InspectResponse {
	return image.InspectResponse{ID: id, Manifests: []image.ManifestSummary{
		manifest("sha256:amd64-of-"+id, "amd64", false),
		manifest(hostManifest, "arm64", true),
		attestation("sha256:att-of-" + id),
	}}
}

// Each row is an image store, a Compose release or an image shape the label
// can come from, and what Compose records for the next image.
func TestTheNextComposeImageIdentityFollowsTheRuleThatMadeTheLabel(t *testing.T) {
	singleManifest := func(id string) image.InspectResponse {
		return image.InspectResponse{ID: id, Manifests: []image.ManifestSummary{manifest(id, "arm64", true)}}
	}
	cases := []struct {
		name     string
		recorded string
		old      image.InspectResponse
		next     image.InspectResponse
		want     string // empty: the label cannot be derived
	}{
		{
			name:     "containerd store, Compose from 5.4: the host manifest",
			recorded: "sha256:old-arm64",
			old:      index("sha256:old-index", "sha256:old-arm64"),
			next:     index("sha256:new-index", "sha256:new-arm64"),
			want:     "sha256:new-arm64",
		},
		{
			name:     "containerd store, Compose before 5.4: the image ID",
			recorded: "sha256:old-index",
			old:      index("sha256:old-index", "sha256:old-arm64"),
			next:     index("sha256:new-index", "sha256:new-arm64"),
			want:     "sha256:new-index",
		},
		{
			name:     "classic store: no manifests, the image ID",
			recorded: "sha256:old-config",
			old:      image.InspectResponse{ID: "sha256:old-config"},
			next:     image.InspectResponse{ID: "sha256:new-config"},
			want:     "sha256:new-config",
		},
		{
			name:     "single manifests on both sides: every rule agrees",
			recorded: "sha256:old-single",
			old:      singleManifest("sha256:old-single"),
			next:     singleManifest("sha256:new-single"),
			want:     "sha256:new-single",
		},
		{
			name:     "a single manifest followed by an index: the ID and the manifest rules disagree",
			recorded: "sha256:old-single",
			old:      singleManifest("sha256:old-single"),
			next:     index("sha256:new-index", "sha256:new-arm64"),
		},
		{
			name:     "a label no rule explains, left by an earlier replacement",
			recorded: "sha256:older-arm64",
			old:      index("sha256:old-index", "sha256:old-arm64"),
			next:     index("sha256:new-index", "sha256:new-arm64"),
		},
		{
			name:     "a pinned platform among several available manifests",
			recorded: "sha256:old-amd64",
			old: image.InspectResponse{ID: "sha256:old-index", Manifests: []image.ManifestSummary{
				manifest("sha256:old-amd64", "amd64", true), manifest("sha256:old-arm64", "arm64", true),
			}},
			next: image.InspectResponse{ID: "sha256:new-index", Manifests: []image.ManifestSummary{
				manifest("sha256:new-amd64", "amd64", true), manifest("sha256:new-arm64", "arm64", false),
			}},
			want: "sha256:new-amd64",
		},
		{
			name:     "several manifests available in the new image: the choice depends on the platform",
			recorded: "sha256:old-arm64",
			old:      index("sha256:old-index", "sha256:old-arm64"),
			next: image.InspectResponse{ID: "sha256:new-index", Manifests: []image.ManifestSummary{
				manifest("sha256:new-amd64", "amd64", true), manifest("sha256:new-arm64", "arm64", true),
			}},
		},
		{
			name:     "no label to follow",
			recorded: "",
			old:      index("sha256:old-index", "sha256:old-arm64"),
			next:     index("sha256:new-index", "sha256:new-arm64"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nextComposeImageIdentity(tc.recorded, tc.old, tc.next)
			require.Equal(t, tc.want != "", ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// imageDaemon answers image inspects from a fixed set of images, with or
// without their manifests, the way engines before and from API 1.48 do.
type imageDaemon struct {
	*fakeDockerClient
	images       map[string]image.InspectResponse
	noManifests  bool // an engine before API 1.48 refuses the manifests option
	missing      map[string]bool
	withOptions  int
	plainInspect int
}

func (d *imageDaemon) ImageInspect(_ context.Context, ref string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	// the only option the updater passes is the manifests one
	if len(opts) > 0 {
		d.withOptions++
		if d.noManifests {
			return client.ImageInspectResult{}, errors.New(`"manifests" requires API version 1.48`)
		}
	} else {
		d.plainInspect++
	}
	img, ok := d.images[ref]
	if !ok || d.missing[ref] {
		return client.ImageInspectResult{}, errors.New("no such image: " + ref)
	}
	if len(opts) == 0 {
		img.Manifests = nil
	}
	return client.ImageInspectResult{InspectResponse: img}, nil
}

// updater wires a Service onto the daemon. Nothing here waits on events: a
// stopped container is swapped without a health verification.
func (d *imageDaemon) updater() *Service { return &Service{client: d} }

func replacedContainer(label string) container.InspectResponse {
	labels := map[string]string{api.ProjectLabel: "stack"}
	if label != "" {
		labels[api.ImageDigestLabel] = label
	}
	return container.InspectResponse{
		Name:   "/stack-app-1",
		Image:  "sha256:old-index",
		Config: &container.Config{Image: "app:latest", Labels: labels},
	}
}

func TestAReplacementCarriesTheComposeIdentityOfItsImage(t *testing.T) {
	daemon := &imageDaemon{fakeDockerClient: newFakeDockerClient(), images: map[string]image.InspectResponse{
		"sha256:old-index": index("sha256:old-index", "sha256:old-arm64"),
		"app:latest":       index("sha256:new-index", "sha256:new-arm64"),
	}}
	service := daemon.updater()
	old := replacedContainer("sha256:old-arm64")

	labels := service.replacementLabels(t.Context(), "app:latest", old)
	require.Equal(t, "sha256:new-arm64", labels[api.ImageDigestLabel])
	require.Equal(t, "stack", labels[api.ProjectLabel], "the other labels are carried over")
	require.Equal(t, "sha256:old-arm64", old.Config.Labels[api.ImageDigestLabel],
		"the replaced container's configuration must not be modified")
}

// The replacement goes through containerCreate, which the whole transaction
// shares: a stopped container is swapped the same way.
func TestTheRecreateTransactionCreatesTheReplacementWithTheNewIdentity(t *testing.T) {
	daemon := &imageDaemon{fakeDockerClient: newFakeDockerClient(), images: map[string]image.InspectResponse{
		"sha256:old-index": index("sha256:old-index", "sha256:old-arm64"),
		"app:v2":           index("sha256:new-index", "sha256:new-arm64"),
	}}
	daemon.add("old000000000a", "app", false, nil)
	daemon.containers["old000000000a"].config = replacedContainer("sha256:old-arm64").Config
	daemon.containers["old000000000a"].image = "sha256:old-index"
	service := daemon.updater()

	require.NoError(t, service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), false))
	for _, item := range daemon.containers {
		require.Equal(t, "app", item.name)
		require.Equal(t, "sha256:new-arm64", item.config.Labels[api.ImageDigestLabel])
	}
}

func TestAnEngineWithoutManifestsGetsTheImageIDOnBothSides(t *testing.T) {
	daemon := &imageDaemon{fakeDockerClient: newFakeDockerClient(), noManifests: true, images: map[string]image.InspectResponse{
		"sha256:old-index": index("sha256:old-index", "sha256:old-arm64"),
		"app:latest":       index("sha256:new-index", "sha256:new-arm64"),
	}}
	service := daemon.updater()

	labels := service.replacementLabels(t.Context(), "app:latest", replacedContainer("sha256:old-index"))
	require.Equal(t, "sha256:new-index", labels[api.ImageDigestLabel])
	require.Equal(t, 1, daemon.withOptions, "the manifests option is tried once, not per image")
	require.Equal(t, 2, daemon.plainInspect)
}

func TestTheLabelIsLeftAloneWhenItCannotBeDerived(t *testing.T) {
	images := map[string]image.InspectResponse{
		"sha256:old-index": index("sha256:old-index", "sha256:old-arm64"),
		"app:latest":       index("sha256:new-index", "sha256:new-arm64"),
	}
	t.Run("the new image cannot be inspected", func(t *testing.T) {
		daemon := &imageDaemon{fakeDockerClient: newFakeDockerClient(), images: images, missing: map[string]bool{"app:latest": true}}
		labels := daemon.updater().replacementLabels(t.Context(), "app:latest", replacedContainer("sha256:old-arm64"))
		require.Equal(t, "sha256:old-arm64", labels[api.ImageDigestLabel])
	})
	t.Run("the old image is gone", func(t *testing.T) {
		daemon := &imageDaemon{fakeDockerClient: newFakeDockerClient(), images: images, missing: map[string]bool{"sha256:old-index": true}}
		labels := daemon.updater().replacementLabels(t.Context(), "app:latest", replacedContainer("sha256:old-arm64"))
		require.Equal(t, "sha256:old-arm64", labels[api.ImageDigestLabel])
	})
	t.Run("a container Compose did not create", func(t *testing.T) {
		daemon := &imageDaemon{fakeDockerClient: newFakeDockerClient(), images: images}
		labels := daemon.updater().replacementLabels(t.Context(), "app:latest", replacedContainer(""))
		require.NotContains(t, labels, api.ImageDigestLabel, "Dockman must not start labelling containers itself")
		require.Zero(t, daemon.withOptions+daemon.plainInspect, "nothing to follow, nothing to inspect")
	})
}
