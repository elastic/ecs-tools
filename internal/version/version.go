// Licensed to Elasticsearch B.V. under one or more agreements.
// Elasticsearch B.V. licenses this file to you under the Apache 2.0 License.
// See the LICENSE file in the project root for more information.

// Package version provides version information.
package version

import "runtime/debug"

var (
	Version = "none"
	Commit  = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	Version = info.Main.Version
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			Commit = setting.Value
			break
		}
	}
}
