package main

import "regexp"

// release is the repository's VERSION file; scripts/release/version.mjs
// --write keeps this copy in step.
const release = "2.1.6"

var semver = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

// releaseVersion is the version shown in Settings › About: the ldflags
// version of a release build, else (development builds, deployments that
// stamp a commit) the VERSION copy above.
func releaseVersion() string {
	if semver.MatchString(version) {
		return version
	}
	return release
}
