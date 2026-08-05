package secrets

// Service keeps the public contract provider-neutral. Plain files are the
// first implementation; SOPS/age will materialize through the same store.
type Service struct{ runtime Store }

func NewService(runtime Store) *Service { return &Service{runtime: runtime} }

func (s *Service) List(host, stackPath string) ([]Metadata, error) {
	return s.runtime.List(host, stackPath)
}

func (s *Service) Read(host, stackPath, name string) ([]byte, error) {
	return s.runtime.Read(host, stackPath, name)
}

func (s *Service) Write(host, stackPath, name string, value []byte) (Metadata, error) {
	return s.runtime.Write(host, stackPath, name, value)
}

func (s *Service) Delete(host, stackPath, name string) error {
	return s.runtime.Delete(host, stackPath, name)
}
