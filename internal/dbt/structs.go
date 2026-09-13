package dbt

import "strings"

// StructField is one field of a struct-typed column, named by its full dotted
type StructField struct {
	// Path is the dotted name dbt documents the field under, e.g.
	Path string
	// Type is the field's own warehouse type.
	Type string
}

// StructFields expands a struct-typed column into the dotted field paths dbt documents
// nested data under, and returns nil for any other type, so callers can pass every
// column through it.
func StructFields(column, dataType string) []StructField {
	var out []StructField
	collectStructFields(column, dataType, &out, 0)
	return out
}

// maxStructDepth stops a pathological or malformed type from recursing forever.
const maxStructDepth = 16

func collectStructFields(prefix, dataType string, out *[]StructField, depth int) {
	if depth > maxStructDepth {
		return
	}
	body, ok := structBody(dataType)
	if !ok {
		return
	}
	for _, field := range splitFields(body) {
		name, fieldType, ok := splitNameAndType(field)
		if !ok {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		*out = append(*out, StructField{Path: path, Type: fieldType})
		collectStructFields(path, fieldType, out, depth+1)
	}
}

// structBody returns the inside of a struct type declaration, unwrapping any
func structBody(dataType string) (string, bool) {
	t := strings.TrimSpace(dataType)

	// `STRUCT(...)[]` and `ARRAY<STRUCT<...>>` both mean a repeated record.
	for {
		trimmed := strings.TrimSpace(t)
		if strings.HasSuffix(trimmed, "[]") {
			t = strings.TrimSuffix(trimmed, "[]")
			continue
		}
		if inner, ok := unwrapKeyword(trimmed, "array"); ok {
			t = inner
			continue
		}
		break
	}

	for _, keyword := range []string{"struct", "object", "row"} {
		if inner, ok := unwrapKeyword(t, keyword); ok {
			return inner, true
		}
	}
	return "", false
}

// unwrapKeyword matches `KEYWORD(...)` or `KEYWORD<...>` and returns the inside.
func unwrapKeyword(t, keyword string) (string, bool) {
	t = strings.TrimSpace(t)
	if len(t) <= len(keyword) || !strings.EqualFold(t[:len(keyword)], keyword) {
		return "", false
	}
	rest := strings.TrimSpace(t[len(keyword):])
	if len(rest) < 2 {
		return "", false
	}
	var closing byte
	switch rest[0] {
	case '(':
		closing = ')'
	case '<':
		closing = '>'
	default:
		return "", false
	}
	if rest[len(rest)-1] != closing {
		return "", false
	}
	return rest[1 : len(rest)-1], true
}

// splitFields splits a struct body on the commas that separate fields, ignoring
func splitFields(body string) []string {
	var fields []string
	depth := 0
	start := 0
	var quote byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case '(', '<', '[':
			depth++
		case ')', '>', ']':
			depth--
		case ',':
			if depth == 0 {
				fields = append(fields, body[start:i])
				start = i + 1
			}
		}
	}
	if start < len(body) {
		fields = append(fields, body[start:])
	}
	return fields
}

// splitNameAndType separates `name TYPE`, allowing the name to be quoted and
func splitNameAndType(field string) (string, string, bool) {
	f := strings.TrimSpace(field)
	if f == "" {
		return "", "", false
	}

	if f[0] == '"' || f[0] == '`' {
		quote := f[0]
		if end := strings.IndexByte(f[1:], quote); end >= 0 {
			name := f[1 : 1+end]
			return name, strings.TrimSpace(f[2+end:]), name != ""
		}
		return "", "", false
	}

	// An anonymous field is just a type, with no name to document.
	i := strings.IndexAny(f, " \t")
	if i < 0 {
		return "", "", false
	}
	return f[:i], strings.TrimSpace(f[i+1:]), true
}
