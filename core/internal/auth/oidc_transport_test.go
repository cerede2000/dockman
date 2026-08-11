package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/RA341/dockman/internal/info"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// skipsCertificateVerification reports whether the context carries an HTTP
// client that would accept any certificate from the identity provider.
func skipsCertificateVerification(ctx context.Context) bool {
	client, ok := ctx.Value(oauth2.HTTPClient).(*http.Client)
	if !ok || client == nil {
		return false
	}
	transport, ok := client.Transport.(*http.Transport)
	return ok && transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify
}

// The OIDC client skips certificate verification on the development flavour so
// a self-signed identity provider can be used locally. On a released build it
// must not: an attacker on the network path to the provider would otherwise be
// able to present any certificate and forge the identity Dockman trusts.
func TestTheOIDCClientVerifiesCertificatesOnReleasedFlavours(t *testing.T) {
	previous := info.Flavour
	t.Cleanup(func() { info.SetFlavour(previous) })

	for _, flavour := range []info.FlavourType{info.FlavourServer, info.FlavourDocker, info.FlavourDesktop} {
		info.SetFlavour(flavour)
		require.False(t, skipsCertificateVerification(getOidcContext(context.Background())),
			"flavour %q must verify the identity provider's certificate", flavour)
	}
}

// And the relaxation still has to be there for local development, or the
// guard above would be measuring nothing.
func TestTheOIDCClientIsRelaxedOnlyForDevelopment(t *testing.T) {
	previous := info.Flavour
	t.Cleanup(func() { info.SetFlavour(previous) })

	info.SetFlavour(info.FlavourDevelop)
	require.True(t, skipsCertificateVerification(getOidcContext(context.Background())))
}

// Whatever it does with the transport, it must never lose the caller's
// context: a cancelled sign-in has to stop reaching the provider.
func TestTheOIDCContextKeepsCancellation(t *testing.T) {
	previous := info.Flavour
	t.Cleanup(func() { info.SetFlavour(previous) })

	for _, flavour := range []info.FlavourType{info.FlavourDevelop, info.FlavourServer} {
		info.SetFlavour(flavour)
		ctx, cancel := context.WithCancel(context.Background())
		derived := getOidcContext(ctx)
		cancel()
		require.Error(t, derived.Err(), "flavour %q dropped the caller's context", flavour)
	}
}

