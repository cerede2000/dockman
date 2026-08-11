package info

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Flavour decides whether development-only relaxations are on - the OIDC
// client that skips certificate verification above all. It used to default to
// "develop", so an entry point that forgot app.InitMeta would silently have
// run with them. A default has to fail closed: the relaxations are opted into,
// never inherited.
func TestTheDefaultFlavourEnablesNoDevelopmentRelaxation(t *testing.T) {
	previous := Flavour
	t.Cleanup(func() { SetFlavour(previous) })

	// The package's own initial value, as a binary that never called
	// InitMeta would see it.
	Flavour = defaultFlavour

	require.False(t, IsDev(), "the default flavour must not enable development behaviour")
}

func TestDevelopmentIsOptedIntoExplicitly(t *testing.T) {
	previous := Flavour
	t.Cleanup(func() { SetFlavour(previous) })

	SetFlavour(FlavourDevelop)
	require.True(t, IsDev())

	for _, flavour := range []FlavourType{FlavourServer, FlavourDocker, FlavourDesktop} {
		SetFlavour(flavour)
		require.False(t, IsDev(), "flavour %q must not enable development behaviour", flavour)
	}
}
