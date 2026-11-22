// Command indexcheck refuses a channel index that keelbox could not read,
// before scripts/publish.sh deploys it: JSON keelbox's own decoder accepts,
// with no unknown field, a release version, a release host keelbox
// downloads from, and only security notices clients would show.
//
//	indexcheck stable.json
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/keelbox/keelbox/internal/release"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: indexcheck stable.json")
		os.Exit(2)
	}
	if err := check(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "indexcheck: %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func check(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G703: the maintainer names the file to check
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var idx release.Index
	if err := dec.Decode(&idx); err != nil {
		return err
	}
	if !strings.HasPrefix(idx.Version, "v") || !release.IsRelease(idx.Version) {
		return fmt.Errorf("version %q is not a release", idx.Version)
	}
	if !slices.Contains(release.ReleaseHosts, strings.TrimRight(idx.Releases, "/")) {
		return fmt.Errorf("release host %q is not one keelbox downloads from", idx.Releases)
	}
	for _, n := range idx.Security {
		if !n.Valid() {
			return fmt.Errorf("clients would not show the security notice %+v: fix get/security.json (docs/reference/release.md)", n)
		}
	}
	return nil
}
