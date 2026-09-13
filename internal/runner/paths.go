package runner

import (
	"path"
	"strconv"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// TargetSchemaPath returns the repo-relative YAML path a node should be documented in,
// or ok=false when no template applies, in which case the node stays where it is.
func TargetSchemaPath(n *dbt.Node) (string, bool) {
	if n.IsSource() || n.Project == nil {
		return "", false
	}
	tpl, ok := n.Project.PathTemplate(n)
	if !ok {
		return "", false
	}

	rendered := renderTemplate(tpl, n)
	if rendered == "" {
		return "", false
	}
	if !strings.HasSuffix(rendered, ".yml") && !strings.HasSuffix(rendered, ".yaml") {
		rendered += ".yml"
	}

	src := path.Clean(strings.ReplaceAll(n.OriginalFilePath, "\\", "/"))
	if strings.HasPrefix(rendered, "/") {
		root := src
		if i := strings.Index(src, "/"); i >= 0 {
			root = src[:i]
		}
		return path.Join(root, strings.TrimPrefix(rendered, "/")), true
	}
	return path.Join(path.Dir(src), rendered), true
}

// renderTemplate expands the `{...}` placeholders in a path template.
func renderTemplate(tpl string, n *dbt.Node) string {
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		if tpl[i] != '{' {
			b.WriteByte(tpl[i])
			continue
		}
		end := strings.IndexByte(tpl[i:], '}')
		if end < 0 {
			b.WriteString(tpl[i:])
			break
		}
		key := tpl[i+1 : i+end]
		b.WriteString(expand(key, n))
		i += end
	}
	return b.String()
}

func expand(key string, n *dbt.Node) string {
	src := strings.ReplaceAll(n.OriginalFilePath, "\\", "/")
	switch key {
	case "model", "name":
		return n.Name
	case "parent":
		return path.Base(path.Dir(src))
	case "schema":
		return n.Schema
	case "database":
		return n.Database
	case "alias":
		return n.Relation()
	}
	// `fqn[-2]`, and the dbt-osmosis spelling `node.fqn[-2]`.
	key = strings.TrimPrefix(key, "node.")
	if strings.HasPrefix(key, "fqn[") && strings.HasSuffix(key, "]") {
		idx, err := strconv.Atoi(key[len("fqn[") : len(key)-1])
		if err != nil {
			return ""
		}
		if idx < 0 {
			idx += len(n.FQN)
		}
		if idx < 0 || idx >= len(n.FQN) {
			return ""
		}
		return n.FQN[idx]
	}
	return ""
}
