package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// A dbt project that installs this tool from PyPI already has a pyproject.toml,
// and PEP 518 reserves `[tool.<name>]` in it for exactly this. Keeping the
// configuration next to the dependency that provides it saves a file whose only
// job is to exist.
//
// The table holds the same keys as dbt_ditto.yml, spelled the way TOML spells
// them: a list of projects is `[[tool.dbt-ditto.projects]]`, a nested section is
// `[tool.dbt-ditto.inheritance]`.
const (
	PyprojectFilename = "pyproject.toml"
	// PyprojectTable is the table read, named after the distribution.
	PyprojectTable = "tool.dbt-ditto"
	// pyprojectTableAlias is accepted too: TOML bare keys allow both spellings,
	// and a project that writes the underscore form means the same thing.
	pyprojectTableAlias = "dbt_ditto"
)

func isPyproject(path string) bool {
	return strings.EqualFold(filepath.Base(path), PyprojectFilename)
}

// hasPyprojectTable reports whether a pyproject.toml configures this tool. A
// file that cannot be parsed is treated as not configuring it: someone else's
// broken packaging file is not this tool's error to raise, and the search should
// carry on to a config that does exist.
func hasPyprojectTable(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, ok, err := pyprojectSection(raw)
	return err == nil && ok
}

// unmarshalPyproject fills c from the `[tool.dbt-ditto]` table.
//
// The table is decoded to a generic map and handed back to the YAML decoder
// rather than given a second set of struct tags. One set of tags cannot drift
// from the other, and every default, alias and nested section keeps exactly one
// definition.
func unmarshalPyproject(raw []byte, c *Config) error {
	section, ok, err := pyprojectSection(raw)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no [%s] table", PyprojectTable)
	}
	intermediate, err := yaml.Marshal(section)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(intermediate, c)
}

// pyprojectSection returns the tool table, if the file has one.
func pyprojectSection(raw []byte) (map[string]any, bool, error) {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, false, err
	}
	tool, ok := doc["tool"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	// `dbt-ditto` is the distribution name and the spelling to document;
	// `dbt_ditto` is accepted because the two are the same name to anyone
	// writing it.
	for _, key := range []string{"dbt-ditto", pyprojectTableAlias} {
		if section, ok := tool[key].(map[string]any); ok {
			return section, true, nil
		}
	}
	return nil, false, nil
}
