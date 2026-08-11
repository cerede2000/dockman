package updater

import (
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

func labelled(value string) map[string]string {
	return map[string]string{DockmanContainerLabel: value}
}

// The label is baked into the Dockman image, so in practice it reads "true".
// It can still be overridden in a Compose file or in a modified image, and the
// two questions asked of it want opposite treatment of a value that cannot be
// read - which is why they are two functions rather than one.
//
// Protection asks "must this container be kept away from an ordinary update?".
// Getting that wrong means recreating Dockman through the API it is itself
// serving, so anything unreadable counts as yes. This is the same rule
// dockman.update.disable already follows.
func TestProtectionTreatsAnUnreadableValueAsDockman(t *testing.T) {
	for _, value := range []string{"true", "1", "yes", "on", "enabled", "", "maybe", "TRUE"} {
		cont := container.Summary{Labels: labelled(value)}
		require.True(t, MarksDockmanContainer(&cont),
			"dockman.container=%q must be protected", value)
	}
}

func TestProtectionStillRespectsAnExplicitFalse(t *testing.T) {
	// The self-update helper deliberately labels itself false so Dockman never
	// mistakes it for itself.
	for _, value := range []string{"false", "0", "no", "off", "disabled"} {
		cont := container.Summary{Labels: labelled(value)}
		require.False(t, MarksDockmanContainer(&cont),
			"dockman.container=%q must not be protected", value)
	}
	require.False(t, MarksDockmanContainer(&container.Summary{Labels: map[string]string{}}),
		"a container without the label is not Dockman")
}

// Identification asks "is this the container to restart?". Getting that wrong
// means recreating somebody else's container, so only an unambiguous value
// counts and anything unreadable is refused.
func TestIdentificationAcceptsOnlyAnUnambiguousValue(t *testing.T) {
	for _, value := range []string{"true", "1", "yes", "on", "enabled", ""} {
		require.True(t, IdentifiesDockmanContainer(labelled(value)),
			"dockman.container=%q identifies Dockman", value)
	}
	for _, value := range []string{"false", "0", "no", "maybe", "unknown"} {
		require.False(t, IdentifiesDockmanContainer(labelled(value)),
			"dockman.container=%q must not select a container to recreate", value)
	}
	require.False(t, IdentifiesDockmanContainer(map[string]string{}))
}

// The two guards must not disagree about the label the image actually carries.
func TestBothAgreeOnTheImageLabel(t *testing.T) {
	cont := container.Summary{Labels: labelled("true")}
	require.True(t, MarksDockmanContainer(&cont))
	require.True(t, IdentifiesDockmanContainer(cont.Labels))
}
