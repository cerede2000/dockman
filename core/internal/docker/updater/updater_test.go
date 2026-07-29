package updater

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
)

func TestValidateHealthcheckHostAllowsOnlyContainerAndLoopbackAddresses(t *testing.T) {
	inspect := &container.InspectResponse{NetworkSettings: &container.NetworkSettings{
		Networks: map[string]*network.EndpointSettings{"default": {IPAddress: netip.MustParseAddr("172.18.0.4")}},
	}}
	require.NoError(t, validateHealthcheckHost("localhost", inspect))
	require.NoError(t, validateHealthcheckHost("127.0.0.1", inspect))
	require.NoError(t, validateHealthcheckHost("172.18.0.4", inspect))
	require.Error(t, validateHealthcheckHost("169.254.169.254", inspect))
	require.Error(t, validateHealthcheckHost("internal.example", inspect))
}

func TestWithConfigCopiesProvidedConfiguration(t *testing.T) {
	expected := &containersUpdateConfig{AllowSelfUpdate: true, ForceUpdate: true, NotifyOnlyMode: true, optInUpdates: true}
	actual := parseOpts(WithConfig(expected))
	require.Equal(t, expected, actual)
}

func TestSummaryNameFallsBackToShortID(t *testing.T) {
	require.Equal(t, "0123456789ab", summaryName(container.Summary{ID: "0123456789abcdef"}))
	require.Equal(t, "named", summaryName(container.Summary{ID: "0123456789abcdef", Names: []string{"/named"}}))
}
