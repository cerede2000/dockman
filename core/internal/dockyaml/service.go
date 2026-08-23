package dockyaml

import (
	"io"

	"github.com/RA341/dockman/pkg/fileutil"
	"time"

	"dario.cat/mergo"
	"github.com/RA341/dockman/pkg/syncmap"
	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog/log"
)

type CachedYaml struct {
	yam         *DockmanYaml
	prevModTime time.Time
}

type Service struct {
	store  Store
	cached syncmap.Map[string, *CachedYaml]
}

func New(store Store) *Service {
	return &Service{
		store: store,
	}
}

func (s *Service) GetYaml(host string) *DockmanYaml {
	// Start with defaults, then merge the loaded config
	config := defaultDockmanYaml
	var override DockmanYaml

	file, state, err := s.store.Get(host)
	if err != nil {
		log.Warn().Err(err).Str("host", host).Msg("Failed to load dockyaml")
		return &config
	}
	// Every path out of here used to leak this handle, including the cache hit
	// just below. GetYaml is read from getSortRank, which runs inside a sort
	// comparator, so listing one directory of N entries leaked O(N log N)
	// descriptors - the process ran out of them and then nothing could open a
	// file or accept a connection any more.
	defer fileutil.Close(file)

	cah, ok := s.cached.Load(host)
	if ok && !state.ModTime().After(cah.prevModTime) {
		//log.Debug().Str("host", host).Msg("using cache")
		return cah.yam
	}

	content, err := io.ReadAll(file)
	if err != nil {
		log.Warn().Err(err).Str("host", host).Msg("Failed to read contents")
		return &config
	}

	err = yaml.Unmarshal(content, &override)
	if err != nil {
		log.Warn().Err(err).Msg("could not unmarshal contents")
		return &config
	}

	if err := mergo.Merge(&config, override, mergo.WithOverride); err != nil {
		log.Warn().Err(err).Msg("failed to merge configs, using defaults")
		return &config
	}

	s.cached.Store(host, &CachedYaml{
		yam:         &config,
		prevModTime: state.ModTime(),
	})

	return &config
}

const defaultYamContent = `# This is an optional configuration file for dockman
# You can use this to customize how dockman looks and behaves
#
# docs: https://dockman.radn.dev/docs/dockman-yaml/overview
`

func (s *Service) GetContents(host string) ([]byte, error) {
	get, _, err := s.store.Get(host)
	if err != nil {
		return nil, err
	}
	defer fileutil.Close(get)

	all, err := io.ReadAll(get)
	if err != nil {
		return nil, err
	}

	if len(all) == 0 {
		return []byte(defaultYamContent), nil
	}

	return all, err
}

func (s *Service) Save(host string, contents []byte) error {
	return s.store.Save(host, contents)
}
