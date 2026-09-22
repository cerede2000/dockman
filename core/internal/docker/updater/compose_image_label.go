package updater

import (
	"context"
	"maps"
	"slices"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

// Compose stamps com.docker.compose.image on every container it creates with
// the identity of the image it resolved for the service, and recreates a
// container whose label differs from what it resolves on the next up
// (mustRecreate, pkg/compose/reconcile.go). containerCreate copies the whole
// configuration of the container it replaces, labels included, so a container
// Dockman had moved to a new image kept the identity of the old one: the next
// Deploy or Git synchronisation recreated it a second time, without the health
// verification or the rollback of the update that had just succeeded.
//
// The replacement therefore carries the identity Compose records for the new
// image. What Compose records has changed between releases and depends on the
// image store (localContentDigest, pkg/compose/images.go):
//   - before 5.4, and on engines older than API 1.48, the image ID;
//   - from 5.4, the digest of the image manifest available locally for the
//     platform, which on the containerd image store is not the image ID (that
//     is the index), and on the classic store, which lists no manifests, is.
//
// Dockman cannot tell which Compose will run next - a remote host runs its
// own, plugin or standalone, at any version - nor which platform a service
// pins. So it does not predict the rule: it finds which rule produced the
// label on the container it replaces, by comparing that label with the image
// the container runs, and applies that rule to the new image. When no rule
// explains the label, or the rules that do disagree on the new image, the
// label is left as it was, and Compose recreates the container on its next up
// as it always did.

// replacementLabels returns the labels of a replacement for old running
// newImage: old's own, with the Compose image identity carried over to the
// new image when it can be derived. old's map is never modified.
func (u *Service) replacementLabels(ctx context.Context, newImage string, old container.InspectResponse) map[string]string {
	var labels map[string]string
	if old.Config != nil {
		labels = old.Config.Labels
	}
	recorded := labels[api.ImageDigestLabel]
	if recorded == "" {
		return labels
	}
	identity, ok := u.composeImageIdentity(ctx, old.Image, newImage, recorded)
	if !ok {
		log.Info().Str("container", old.Name).Str("image", newImage).
			Msgf("cannot tell how Compose identifies the new image; it will recreate this container on its next up")
		return labels
	}
	if identity == recorded {
		return labels
	}
	labels = maps.Clone(labels)
	labels[api.ImageDigestLabel] = identity
	return labels
}

// composeImageIdentity inspects both images the same way and derives the
// identity Compose would record for newImage, given that it recorded
// `recorded` for oldImage.
func (u *Service) composeImageIdentity(ctx context.Context, oldImage, newImage, recorded string) (string, bool) {
	if oldImage == "" {
		return "", false
	}
	// Engines before API 1.48 cannot list manifests, and Compose then records
	// the image ID, which the plain inspect gives. Both images must come from
	// the same kind of inspect, or they are not comparable.
	manifests := []client.ImageInspectOption{client.ImageInspectWithManifests(true)}
	old, err := u.cli().ImageInspect(ctx, oldImage, manifests...)
	if err != nil {
		manifests = nil
		if old, err = u.cli().ImageInspect(ctx, oldImage); err != nil {
			return "", false
		}
	}
	next, err := u.cli().ImageInspect(ctx, newImage, manifests...)
	if err != nil {
		return "", false
	}
	return nextComposeImageIdentity(recorded, old.InspectResponse, next.InspectResponse)
}

// nextComposeImageIdentity keeps each rule under which Compose records
// `recorded` for old, applies it to next, and answers only when every such
// rule gives next the same identity.
func nextComposeImageIdentity(recorded string, old, next image.InspectResponse) (string, bool) {
	if recorded == "" || old.ID == "" || next.ID == "" {
		return "", false
	}
	candidates := map[string]bool{}
	if recorded == old.ID {
		candidates[next.ID] = true
	}
	if slices.Contains(manifestIdentities(old), recorded) {
		for _, identity := range manifestIdentities(next) {
			candidates[identity] = true
		}
	}
	if len(candidates) != 1 {
		return "", false
	}
	for identity := range candidates {
		return identity, true
	}
	return "", false
}

// manifestIdentities lists what Compose from 5.4 can record for img, whatever
// platform it matches against (matchLocalManifest, pkg/compose/images.go):
// the first available image manifest whose platform matches, the only
// available one whatever its platform, and the image ID when none is
// available or none matches. With one available manifest or none, the
// platform makes no difference and the answer is certain; with several, it
// depends on the platform, and every possibility is listed.
func manifestIdentities(img image.InspectResponse) []string {
	var available []image.ManifestSummary
	for _, m := range img.Manifests {
		if m.Kind == image.ManifestKindImage && m.Available {
			available = append(available, m)
		}
	}
	switch len(available) {
	case 0:
		return []string{img.ID}
	case 1:
		return []string{available[0].ID}
	}
	identities := []string{img.ID}
	for _, m := range available {
		// a manifest without image data never matches a platform
		if m.ImageData != nil {
			identities = append(identities, m.ID)
		}
	}
	return identities
}
