package secrets

import (
	"context"
	"errors"

	"github.com/RA341/dockman/internal/host/filesystem"
)

// Service keeps the public contract provider-neutral. Plain files are the
// first implementation; SOPS/age will materialize through the same store.
type Service struct {
	runtime   Store
	encrypted *SOPSProvider
}

func NewService(runtime Store) *Service { return &Service{runtime: runtime} }

func (s *Service) ConfigureSOPS(provider *SOPSProvider) { s.encrypted = provider }

func (s *Service) ComposeEnvironment(ctx context.Context, host string, stackFS filesystem.FileSystem, composeRelpath string) ([]string, error) {
	if s.encrypted == nil {
		return nil, nil
	}
	return s.encrypted.ComposeEnvironment(ctx, host, stackFS, composeRelpath)
}

func (s *Service) inline(host, stackPath string) (bool, error) {
	if s.encrypted == nil {
		return false, nil
	}
	return s.encrypted.InlineEnabled(host, stackPath)
}

func (s *Service) ListManaged(ctx context.Context, host, stackPath string) ([]Metadata, error) {
	if enabled, err := s.inline(host, stackPath); err != nil {
		return nil, err
	} else if enabled {
		return s.encrypted.ListInline(ctx, host, stackPath)
	}
	return s.runtime.List(host, stackPath)
}

func (s *Service) ReadManaged(ctx context.Context, host, stackPath, name string) ([]byte, error) {
	if enabled, err := s.inline(host, stackPath); err != nil {
		return nil, err
	} else if enabled {
		return s.encrypted.ReadInline(ctx, host, stackPath, name)
	}
	return s.runtime.Read(host, stackPath, name)
}

func (s *Service) WriteManaged(ctx context.Context, host, stackPath, name string, value []byte) (Metadata, error) {
	if enabled, err := s.inline(host, stackPath); err != nil {
		return Metadata{}, err
	} else if enabled {
		return s.encrypted.WriteInline(ctx, host, stackPath, name, value)
	}
	return s.runtime.Write(host, stackPath, name, value)
}

func (s *Service) DeleteManaged(ctx context.Context, host, stackPath, name string) error {
	if enabled, err := s.inline(host, stackPath); err != nil {
		return err
	} else if enabled {
		return s.encrypted.DeleteInline(ctx, host, stackPath, name)
	}
	return s.runtime.Delete(host, stackPath, name)
}

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
		return SOPSStatus{SourcePath: SOPSSourceFile, Mode: "materialized"}, nil
	}
	return s.encrypted.Status(host, stackPath)
}

func (s *Service) ExportSOPS(ctx context.Context, host, stackPath string) (SOPSResult, error) {
	if s.encrypted == nil {
		return SOPSResult{}, ErrSOPSUnavailable
	}
	if enabled, err := s.encrypted.InlineEnabled(host, stackPath); err != nil {
		return SOPSResult{}, err
	} else if enabled {
		return SOPSResult{}, errors.New("inline SOPS mode is already the encrypted source of truth; edit its values directly")
	}
	return s.encrypted.Export(ctx, host, stackPath)
}

func (s *Service) MaterializeSOPS(ctx context.Context, host, stackPath string) (SOPSResult, error) {
	if s.encrypted == nil {
		return SOPSResult{}, ErrSOPSUnavailable
	}
	if enabled, err := s.encrypted.InlineEnabled(host, stackPath); err != nil {
		return SOPSResult{}, err
	} else if enabled {
		return SOPSResult{}, errors.New("inline SOPS mode cannot be materialized without disabling it explicitly")
	}
	return s.encrypted.Materialize(ctx, host, stackPath)
}

func (s *Service) EnableInlineSOPS(ctx context.Context, host, stackPath, composeFile string) (SOPSResult, error) {
	if s.encrypted == nil {
		return SOPSResult{}, ErrSOPSUnavailable
	}
	return s.encrypted.EnableInline(ctx, host, stackPath, composeFile)
}

func (s *Service) DisableInlineSOPS(ctx context.Context, host, stackPath string) (SOPSResult, error) {
	if s.encrypted == nil {
		return SOPSResult{}, ErrSOPSUnavailable
	}
	return s.encrypted.DisableInline(ctx, host, stackPath)
}
