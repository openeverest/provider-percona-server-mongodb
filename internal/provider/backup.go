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

package provider

import (
	"github.com/AlekSi/pointer"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// configureBackup configures the backup component based on the instance spec and metadata defaults.
func configureBackup(c *controller.Context) psmdbv1.BackupSpec {
	// TODO: Implement proper backup configuration
	spec, err := c.ProviderSpec()
	if err != nil { //nolint:staticcheck // FIXME: handle error properly
		// return err
	}
	backupImage := controller.GetDefaultImageForComponent(spec, common.ComponentBackupAgent)

	return psmdbv1.BackupSpec{
		Enabled: true,
		Image:   backupImage,
		PITR: psmdbv1.PITRSpec{
			Enabled: false,
		},
		Configuration: psmdbv1.BackupConfig{
			BackupOptions: &psmdbv1.BackupOptions{
				Timeouts: &psmdbv1.BackupTimeouts{Starting: pointer.ToUint32(defaultBackupStartingTimeout)},
			},
		},

		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1G"),
				corev1.ResourceCPU:    resource.MustParse("300m"),
			},
		},
	}
}
