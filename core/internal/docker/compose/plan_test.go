package compose

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	webHash = "1111111111111111111111111111111111111111111111111111111111111111"
	dbHash  = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestParseConfigHashesIgnoresWarningsOnTheSameStream(t *testing.T) {
	// Compose writes warnings on the stream that carries "<service> <hash>".
	// A two-word warning has exactly two fields, so counting fields is not
	// enough: it would be recorded as a service that does not exist, and then
	// handed to `compose up`, where it fails the whole update.
	hashes, err := parseConfigHashes(strings.Join([]string{
		"WARN[0000] obsolete",
		"WARN[0000] the attribute `version` is obsolete",
		"web " + webHash,
		"db  " + dbHash,
		"",
	}, "\n"))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": webHash, "db": dbHash}, hashes)
}

func TestParseConfigHashesFailsWhenNothingWasReported(t *testing.T) {
	// An empty result must be an error, not an empty map: an empty map reads
	// as "this stack declares no service", which makes every running container
	// an orphan and has Compose remove them.
	_, err := parseConfigHashes("WARN[0000] something\n")
	require.Error(t, err)
}

func TestParseServiceShapesDetectsBuildSections(t *testing.T) {
	model := `{"services":{
		"web":{"build":{"context":"."},"image":"proj-web"},
		"db":{"image":"postgres:17"},
		"cache":{"build":null,"image":"redis:8"}
	}}`
	shapes, err := parseServiceShapes([]byte(model))
	require.NoError(t, err)
	require.True(t, shapes["web"].Buildable)
	require.False(t, shapes["db"].Buildable)
	require.False(t, shapes["cache"].Buildable, "an explicit null build section is not a build")
}

func TestParseServiceShapesReadsTheReplicaCount(t *testing.T) {
	// Compose strips scale and deploy.replicas before hashing, so a stack
	// scaled from one to three keeps an unchanged config hash. Without this
	// the extra replicas would never be created.
	model := `{"services":{
		"plain":{"image":"nginx"},
		"scaled":{"image":"nginx","scale":3},
		"deployed":{"image":"nginx","deploy":{"replicas":2}},
		"both":{"image":"nginx","scale":4,"deploy":{"replicas":2}},
		"unreadable":{"image":"nginx","scale":"${REPLICAS}"}
	}}`
	shapes, err := parseServiceShapes([]byte(model))
	require.NoError(t, err)
	require.Equal(t, 1, shapes["plain"].Replicas)
	require.Equal(t, 3, shapes["scaled"].Replicas)
	require.Equal(t, 2, shapes["deployed"].Replicas)
	require.Equal(t, 4, shapes["both"].Replicas, "scale wins over deploy.replicas")
	require.Equal(t, 0, shapes["unreadable"].Replicas, "an unread count must not pass for one")
}

func TestParseServiceShapesSkipsOutputBeforeTheDocument(t *testing.T) {
	shapes, err := parseServiceShapes([]byte("WARN[0000] noise\n{\"services\":{\"web\":{\"build\":{}}}}"))
	require.NoError(t, err)
	require.True(t, shapes["web"].Buildable)
}
