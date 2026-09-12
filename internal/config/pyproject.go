package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// The table holds the same keys as dbt_ditto.yml, spelled the way TOML spells
// them: a list of projects is `[[tool.dbt-ditto.projects]]`, a nested section
// is `[tool.dbt-ditto.inheritance]`.
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

// hasPyprojectTable reports whether a pyproject.toml configures this tool. An
// unparseable file counts as not configuring it, so the search carries on
// rather than raising someone else's broken packaging as this tool's error.
func hasPyprojectTable(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, ok, err := pyprojectSection(raw)
	return err == nil && ok
}

// unmarshalPyproject fills c from the `[tool.dbt-ditto]` table, by decoding to
// a generic map and handing that to the YAML decoder — so the config structs
// need no second set of `toml:` tags to keep in step.
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
	for _, key := range []string{"dbt-ditto", pyprojectTableAlias} {
		if section, ok := tool[key].(map[string]any); ok {
			return section, true, nil
		}
	}
	return nil, false, nil
}
