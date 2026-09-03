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

// package main implements the provider for Percona Server MongoDB Operator.
package main

import (
	"errors"
	"flag"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openeverest/openeverest/v2/provider-runtime/preflight"
	"github.com/openeverest/openeverest/v2/provider-runtime/reconciler"

	"github.com/openeverest/provider-percona-server-mongodb/internal/provider"
)

// main is the entry point for the provider.
func main() {
	var serverPort int
	var metricsBindAddress string
	var preflightSpec string

	flag.IntVar(&serverPort, "server-port", 8082, "The port for the provider HTTP server.")
	flag.StringVar(&metricsBindAddress, "metrics-bind-address", ":8081", "The address the metrics endpoint binds to. Use 0 to disable.")
	flag.StringVar(&preflightSpec, "preflight-spec", "", "Run the upgrade preflight against the target Provider spec at this path and exit. Used by the chart's pre-upgrade hook.")
	flag.Parse()

	l := ctrl.Log.WithName("setup")
	ctx := ctrl.SetupSignalHandler()

	provider := provider.NewPSMDBProviderInterface()

	if preflightSpec != "" {
		if err := preflight.Run(ctx, provider, preflight.Options{TargetSpecFile: preflightSpec}); err != nil {
			if !errors.Is(err, preflight.ErrUpgradeBlocked) {
				l.Error(err, "upgrade preflight could not run")
			}
			os.Exit(1)
		}
		return
	}

	opts := []reconciler.ReconcilerOption{
		// Enable HTTP server for validation endpoint
		reconciler.WithServer(reconciler.ServerConfig{
			Port:           serverPort,
			ValidationPath: "/validate",
		}),
	}

	if metricsBindAddress != "0" {
		opts = append(opts, reconciler.WithMetrics(metricsBindAddress))
	}

	r, err := reconciler.New(ctx, provider, opts...)

	if err != nil {
		l.Error(err, "unable to create reconciler")
		os.Exit(1)
	}

	// Inject the manager's client so watch handlers can
	// list Instance objects that reference MonitoringConfig.
	// TODO: change the way manager is configured so injection is not necessary.
	provider.SetClient(r.GetManager().GetClient())

	if err := r.Start(ctx); err != nil {
		l.Error(err, "unable to start reconciler")
		os.Exit(1)
	}
}
