package policy

import (
	"path/filepath"

	"github.com/corral-sh/corral/internal/config"
)

// Load reads the layers, enforces the trust rules on the project layer, and
// merges over the defaults. Any path may be "" or absent.
//
// Precedence (highest wins): host per-project file (~/.corral/projects/
// <box>.toml, trusted) > project .corral.toml (restricted) > global
// ~/.corral/config.toml (trusted) > defaults. CLI flags are applied by the
// caller. The host file is where per-project privilege belongs: it is owned
// by the user, not by the repository.
func Load(globalPath, hostProjectPath, projectDir string) (*config.Config, error) {
	merged := config.Defaults()
	var sources []string

	if globalPath != "" {
		f, ok, err := config.ReadFile(globalPath)
		if err != nil {
			return nil, err
		}
		if ok {
			merged = config.Merge(merged, f)
			sources = append(sources, globalPath)
		}
	}
	if projectDir != "" {
		p := filepath.Join(projectDir, config.ProjectFileName)
		if err := refuseSymlink(p); err != nil {
			return nil, err
		}
		f, ok, err := config.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if ok {
			if err := checkProjectFile(p, merged, f); err != nil {
				return nil, err
			}
			merged = config.Merge(merged, f)
			sources = append(sources, p)
		}
	}
	if hostProjectPath != "" {
		f, ok, err := config.ReadFile(hostProjectPath)
		if err != nil {
			return nil, err
		}
		if ok {
			merged = config.Merge(merged, f)
			sources = append(sources, hostProjectPath)
		}
	}
	cfg, err := config.Resolve(merged)
	if err != nil {
		return nil, err
	}
	ApplyProfile(cfg)
	cfg.Sources = sources
	return cfg, nil
}
