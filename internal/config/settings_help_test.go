package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestEverySettingIsExplained is the reason `dbt-ditto help config` can be
// trusted: the table and the config structs are compared key by key, so a new
// setting cannot ship without an explanation, and a removed one cannot linger
// in the help as a key that does nothing.
func TestEverySettingIsExplained(t *testing.T) {
	explained := map[string]bool{}
	for _, s := range Settings {
		if explained[s.Key] {
			t.Errorf("two entries for %s", s.Key)
		}
		explained[s.Key] = true
	}

	for _, key := range settingKeys(reflect.TypeOf(Config{}), "") {
		if !explained[key] {
			t.Errorf("%s is a config key with no entry in Settings: `dbt-ditto help config` would not mention it", key)
		}
		delete(explained, key)
	}
	for key := range explained {
		t.Errorf("Settings explains %s, which is not a config key any more", key)
	}
}

// The order the help prints in is the order the structs declare, so a reader
// meets the keys in the same order in the help and in their own YAML.
func TestSettingsAreInStructOrder(t *testing.T) {
	want := settingKeys(reflect.TypeOf(Config{}), "")
	got := make([]string, 0, len(Settings))
	for _, s := range Settings {
		got = append(got, s.Key)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Settings is out of order:\ngot:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestEverySettingSaysWhatItDoes(t *testing.T) {
	for _, s := range Settings {
		if s.Default == "" {
			t.Errorf("%s has no default: say what happens when the key is absent", s.Key)
		}
		// A key is not an explanation of itself. The threshold is deliberately
		// crude: it catches a placeholder, not a style.
		if len(strings.Fields(s.Help)) < 4 {
			t.Errorf("%s has no real explanation: %q", s.Key, s.Help)
		}
		if !strings.HasSuffix(s.Help, ".") {
			t.Errorf("%s: the explanation is a sentence, so it ends in a full stop: %q", s.Key, s.Help)
		}
	}
}

// The help is read in a terminal, so nothing it prints may run past 80 columns.
func TestTheHelpFitsInATerminal(t *testing.T) {
	var out strings.Builder
	if err := SettingsHelp(&out); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		// A URL is one word and cannot be broken; nothing else is excused.
		if len(line) > 80 && !strings.Contains(line, "://") {
			t.Errorf("%d columns: %q", len(line), line)
		}
	}
	for _, s := range Settings {
		leaf := s.Key
		if i := strings.LastIndex(s.Key, "."); i >= 0 {
			leaf = s.Key[i+1:]
		}
		if !strings.Contains(out.String(), leaf) {
			t.Errorf("%s is missing from the printed help", s.Key)
		}
	}
}

// settingKeys returns the dotted yaml paths of every configurable field. It is
// the same walk internal/runner's TestEverySettingIsCovered does, duplicated
// rather than exported: what counts as a setting is this package's own answer,
// and a test helper exported from here would be the only reason for it to be.
func settingKeys(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		name := prefix + tag

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice {
			elem := ft.Elem()
			for elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct {
				out = append(out, settingKeys(elem, name+"[].")...)
				continue
			}
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, settingKeys(ft, name+".")...)
			continue
		}
		out = append(out, name)
	}
	return out
}
