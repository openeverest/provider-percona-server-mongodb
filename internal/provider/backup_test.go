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
	"testing"

	"github.com/stretchr/testify/assert"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
)

func TestSelectMainStorageName(t *testing.T) {
	tests := []struct {
		name     string
		storages []corev1alpha1.InstanceBackupStorage
		want     string
	}{
		{
			name:     "empty slice returns empty string",
			storages: nil,
			want:     "",
		},
		{
			name: "single storage without PITR returns its name",
			storages: []corev1alpha1.InstanceBackupStorage{
				{Name: "s3-main"},
			},
			want: "s3-main",
		},
		{
			name: "no PITR storage — first entry wins",
			storages: []corev1alpha1.InstanceBackupStorage{
				{Name: "first"},
				{Name: "second"},
			},
			want: "first",
		},
		{
			name: "second storage has PITR — it wins over first",
			storages: []corev1alpha1.InstanceBackupStorage{
				{Name: "first"},
				{Name: "second", PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: true}},
			},
			want: "second",
		},
		{
			name: "first storage has PITR — it wins",
			storages: []corev1alpha1.InstanceBackupStorage{
				{Name: "first", PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: true}},
				{Name: "second"},
			},
			want: "first",
		},
		{
			name: "PITR present but disabled — falls back to first",
			storages: []corev1alpha1.InstanceBackupStorage{
				{Name: "first"},
				{Name: "second", PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: false}},
			},
			want: "first",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectMainStorageName(tc.storages)
			assert.Equal(t, tc.want, got)
		})
	}
}
