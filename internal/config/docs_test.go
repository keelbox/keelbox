package config

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// keyPaths lists every YAML key path in the schema, such as
// target.ssh.host, by walking the struct tags.
func keyPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if opts == "inline" {
			out = append(out, keyPaths(f.Type, prefix)...)
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		path := prefix + name
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, keyPaths(ft, path+".")...)
			continue
		}
		out = append(out, path)
	}
	return out
}

var docRowRe = regexp.MustCompile("(?m)^\\| `([a-z_.]+)` \\|")

// TestConfigReferenceListsEveryKey keeps docs/reference/config.md in step
// with the schema: every key has a row, and every row is a key.
func TestConfigReferenceListsEveryKey(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference/config.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range docRowRe.FindAllStringSubmatch(string(data), -1) {
		documented[m[1]] = true
	}
	schema := map[string]bool{}
	for _, p := range keyPaths(reflect.TypeOf(Config{}), "") {
		schema[p] = true
	}
	var missing, unknown []string
	for p := range schema {
		if !documented[p] {
			missing = append(missing, p)
		}
	}
	for p := range documented {
		if !schema[p] {
			unknown = append(unknown, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(missing) > 0 {
		t.Errorf("docs/reference/config.md has no row for: %s", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		t.Errorf("docs/reference/config.md documents keys the schema does not have: %s", strings.Join(unknown, ", "))
	}
}
