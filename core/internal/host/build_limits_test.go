package host

import (
	"testing"

	v1 "github.com/RA341/dockman/generated/host/v1"
	"github.com/RA341/dockman/internal/database"
	"github.com/RA341/dockman/internal/docker/compose"
	"github.com/stretchr/testify/require"
)

// A store on a real database: database.New applies every embedded migration,
// so these tests also prove the columns exist where GORM expects them.
func buildLimitsStore(t *testing.T) Store {
	t.Helper()
	return NewStore(database.New(t.TempDir(), true))
}

func TestBuildLimitsSurviveTheStoreRoundTrip(t *testing.T) {
	store := buildLimitsStore(t)
	conf := Config{Name: "nas", Type: LOCAL, BuildCPULimit: 1.5, BuildMemoryLimit: 2 << 30}
	require.NoError(t, store.Add(&conf))

	saved, err := store.GetByID(conf.ID)
	require.NoError(t, err)
	require.Equal(t, compose.BuildLimits{CPUs: 1.5, MemoryBytes: 2 << 30}, saved.BuildLimits())

	// lifting the limits must write the zeros, not skip them as "unset"
	saved.BuildCPULimit, saved.BuildMemoryLimit = 0, 0
	require.NoError(t, store.Update(&saved))
	lifted, err := store.GetByID(conf.ID)
	require.NoError(t, err)
	require.False(t, lifted.BuildLimits().Active())
}

// Hosts created before the columns existed come back unlimited.
func TestExistingHostsKeepUnlimitedBuilds(t *testing.T) {
	db := database.New(t.TempDir(), true)
	require.NoError(t, db.Exec("INSERT INTO host_config (created_at, updated_at, name, type, enable) VALUES (datetime('now'), datetime('now'), 'legacy', 'local', 1)").Error)

	legacy, err := NewStore(db).Get("legacy")
	require.NoError(t, err)
	require.False(t, legacy.BuildLimits().Active())
}

func TestTheHostAPICarriesBuildLimits(t *testing.T) {
	conf := Config{Name: "nas", Type: LOCAL, BuildCPULimit: 0.5, BuildMemoryLimit: 768 << 20}
	p := conf.ToProto()
	require.Equal(t, 0.5, p.BuildCpuLimit)
	require.Equal(t, int64(768<<20), p.BuildMemoryLimit)

	back := ConfigFromProto(&v1.Host{Name: "nas", Kind: v1.ClientType_LOCAL, BuildCpuLimit: 2, BuildMemoryLimit: 4 << 30})
	require.Equal(t, compose.BuildLimits{CPUs: 2, MemoryBytes: 4 << 30}, back.BuildLimits())
}

// A limit BuildKit cannot run under is refused when saved, not discovered as
// an obscure build failure later.
func TestEditRefusesLimitsABuilderCannotRunUnder(t *testing.T) {
	store := buildLimitsStore(t)
	conf := Config{Name: "nas", Type: LOCAL, BuildCPULimit: 1}
	require.NoError(t, store.Add(&conf))
	srv := &Service{store: store}

	tooSmall := conf
	tooSmall.BuildCPULimit = 0.05
	require.Error(t, srv.Edit(&tooSmall))

	tooLittleMemory := conf
	tooLittleMemory.BuildMemoryLimit = 64 << 20
	require.Error(t, srv.Edit(&tooLittleMemory))

	unchanged, err := store.GetByID(conf.ID)
	require.NoError(t, err)
	require.Equal(t, 1.0, unchanged.BuildCPULimit, "a refused edit writes nothing")
}

// New limits reach the next build of a connected host at once: no reconnect.
func TestEditingLimitsAppliesThemToTheNextBuild(t *testing.T) {
	store := buildLimitsStore(t)
	conf := Config{Name: "nas", Type: LOCAL}
	require.NoError(t, store.Add(&conf))
	srv := &Service{store: store}
	srv.activeClients.Store(conf.Name, &ActiveHost{HostId: conf.ID})
	srv.buildLimits.Store(conf.ID, conf.BuildLimits())

	edited := conf
	edited.BuildCPULimit, edited.BuildMemoryLimit = 2, 1<<30
	require.NoError(t, srv.Edit(&edited))

	limits, ok := srv.buildLimits.Load(conf.ID)
	require.True(t, ok)
	require.Equal(t, compose.BuildLimits{CPUs: 2, MemoryBytes: 1 << 30}, limits)
}

// Renaming a host must not drop its limits: they follow its ID.
func TestRenamingAHostKeepsItsLimits(t *testing.T) {
	store := buildLimitsStore(t)
	conf := Config{Name: "nas", Type: LOCAL, BuildCPULimit: 1.5}
	require.NoError(t, store.Add(&conf))
	// production always wires the hook that re-points what refers to a host
	srv := &Service{store: store, renameHost: func(string, string) (int, error) { return 0, nil }}
	srv.activeClients.Store(conf.Name, &ActiveHost{HostId: conf.ID})

	renamed := conf
	renamed.Name = "nas-2"
	require.NoError(t, srv.Edit(&renamed))

	active, ok := srv.activeClients.Load("nas-2")
	require.True(t, ok)
	limits, ok := srv.buildLimits.Load(active.HostId)
	require.True(t, ok)
	require.Equal(t, 1.5, limits.CPUs)
}
