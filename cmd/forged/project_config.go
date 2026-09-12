package main

import (
	"errors"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/rkbkosp/collaboration-forge/internal/forge"
)

func loadProjectConfig(file, override string) (forge.ProjectConfig, error) {
	var config struct {
		Project forge.ProjectConfig `toml:"project"`
	}
	if file != "" {
		metadata, err := toml.DecodeFile(file, &config)
		if err != nil {
			return config.Project, errors.New("invalid Forge project configuration")
		}
		if len(metadata.Undecoded()) != 0 {
			return config.Project, errors.New("unknown Forge project configuration field")
		}
	}
	if override != "" {
		config.Project.WorktreeRoot = override
	}
	if config.Project.WorktreeRoot != "" && !filepath.IsAbs(config.Project.WorktreeRoot) {
		return config.Project, errors.New("project.worktree_root must be absolute")
	}
	return config.Project, nil
}
