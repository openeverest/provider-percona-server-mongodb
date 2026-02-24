// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This tool generates the Provider CR YAML manifest from the Go-defined metadata.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

func main() {
	output := flag.String("output", "", "Output file path (default: stdout)")
	// TODO: should provider name be configurable or constant?
	// It has to match the name defined in provider.NewPSMDBProviderInterface().
	name := flag.String("name", "psmdb", "Provider name")
	namespace := flag.String("namespace", "", "Namespace (empty for cluster-scoped)")
	flag.Parse()

	metadata := common.PSMDBMetadata()

	if err := metadata.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid metadata: %v\n", err)
		os.Exit(1)
	}

	if *output == "" {
		// Write to stdout
		if err := controller.GenerateManifestToStdout(metadata, *name, *namespace); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		return
	}

	// Write to file
	if err := controller.GenerateManifest(metadata, *name, *namespace, *output); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Generated: %s\n", *output)
}
