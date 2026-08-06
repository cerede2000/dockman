package secrets

import "context"

// Service keeps the public contract provider-neutral. Plain files are the
// first implementation; SOPS/age will materialize through the same store.
type Service struct {
	runtime   Store
	encrypted *SOPSProvider
}

func NewService(runtime Store) *Service { return &Service{runtime: runtime} }

func (s *Service) ConfigureSOPS(provider *SOPSProvider) { s.encrypted = provider }

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

func (s *Service) ListHistory(host, stackPath, name string) ([]Version, error) {
	return s.runtime.ListHistory(host, stackPath, name)
}

func (s *Service) Restore(host, stackPath, name, version string) (Metadata, error) {
	return s.runtime.Restore(host, stackPath, name, version)
}

func (s *Service) AnalyzeCompose(host, stackPath string) (ComposeAnalysis, error) {
	return s.runtime.AnalyzeCompose(host, stackPath)
}

func (s *Service) ListArchived(host, stackPath string) ([]ArchivedSecret, error) {
	return s.runtime.ListArchived(host, stackPath)
}

func (s *Service) ListStacks(host string) ([]StackOption, error) {
	return s.runtime.ListStacks(host)
}

func (s *Service) SOPSStatus(host, stackPath string) (SOPSStatus, error) {
	if s.encrypted == nil {
		return SOPSStatus{SourcePath: SOPSSourceFile}, nil
	}
	return s.encrypted.Status(host, stackPath)
}

func (s *Service) ExportSOPS(ctx context.Context, host, stackPath string) (SOPSResult, error) {
	if s.encrypted == nil {
		return SOPSResult{}, ErrSOPSUnavailable
	}
	return s.encrypted.Export(ctx, host, stackPath)
}

func (s *Service) MaterializeSOPS(ctx context.Context, host, stackPath string) (SOPSResult, error) {
	if s.encrypted == nil {
		return SOPSResult{}, ErrSOPSUnavailable
	}
	return s.encrypted.Materialize(ctx, host, stackPath)
}
