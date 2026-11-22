// Package testbuild holds the switches of a keelbox built for its own
// end-to-end tests (go build -tags keelbox_testkeys). Such a build also
// trusts the keys of a throwaway release key file the environment names; a
// release build has none of this, and the release workflow refuses a binary
// built with the tag.
package testbuild

import "os"

// Getenv returns an environment variable only in a test build, and "" in
// every other build, so a release binary cannot be made to trust another
// key by its environment.
func Getenv(name string) string {
	if !Enabled {
		return ""
	}
	return os.Getenv(name)
}

// Test build environment.
const (
	// ReleaseKeysEnv names a keys.pem (releasesign testkey) whose keys are
	// trusted as well as the release keys.
	ReleaseKeysEnv = "KEELBOX_TEST_RELEASE_KEYS"
)
