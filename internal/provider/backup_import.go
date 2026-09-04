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
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// pbmMetadataSuffix is the object-key suffix of the per-backup metadata
// documents written by Percona Backup for MongoDB.
const pbmMetadataSuffix = ".pbm.json"

// pbmStatusDone is the PBM backup status that marks a backup as complete and
// restorable. Only backups in this state are surfaced as importable.
const pbmStatusDone = "done"

// maxPBMMetaSize is the maximum size of a *.pbm.json metadata document.
// Give generous cap for *.pbm.json files which are a few KB each.
const maxPBMMetaSize = 1 << 20 // 1 MiB

// pbmBackupMeta is the Percona Backup for MongoDB's per-backup
// metadata document (<name>.pbm.json).
type pbmBackupMeta struct {
	// Status is the backup lifecycle state; only "done" is restorable.
	Status string `json:"status"`
	// StartTS is the backup start time in Unix seconds.
	StartTS int64 `json:"start_ts"`
	// LastTransitionTS is the Unix-seconds time of the last status transition;
	// for a "done" backup this is its completion time.
	LastTransitionTS int64 `json:"last_transition_ts"`
}

// ImportBackups implements controller.BackupImporter for PSMDB. It lists the
// BackupStorage, decodes PBM metadata document (*.pbm.json), keeps the
// backups PBM marks "done", and returns Backup CRs to create.
func (p *PSMDBProvider) ImportBackups(
	ctx context.Context,
	imp *backupv1alpha1.BackupImport,
	storage *backupv1alpha1.BackupStorage,
) (controller.BackupImportExecutionStatus, error) {
	l := log.FromContext(ctx).WithValues("backupImport", imp.Name, "storage", storage.Name)

	s3c, err := controller.NewS3Client(ctx, p.client, storage)
	if err != nil {
		return controller.BackupImportExecutionStatus{}, fmt.Errorf("build S3 client: %w", err)
	}

	bucket := storage.Spec.S3.Bucket

	var backups []*backupv1alpha1.Backup

	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})

	for paginator.HasMorePages() {
		page, perr := paginator.NextPage(ctx)
		if perr != nil {
			return controller.BackupImportExecutionStatus{}, fmt.Errorf("list objects in bucket %q: %w", bucket, perr)
		}

		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if !strings.HasSuffix(key, pbmMetadataSuffix) {
				continue
			}

			data, err := getObject(ctx, s3c, bucket, key)
			if err != nil {
				return controller.BackupImportExecutionStatus{}, fmt.Errorf("read %q: %w", key, err)
			}

			backup, err := parsePBM(imp, key, data)
			if err != nil {
				l.Error(err, "skipping unparseable PBM metadata", "key", key)

				continue
			}

			if backup == nil {
				continue
			}

			backups = append(backups, backup)
		}
	}

	l.Info("discovered PBM backups", "count", len(backups))
	return controller.BackupImportExecutionStatus{
		Backups: backups,
		State:   backupv1alpha1.BackupImportStateSucceeded,
	}, nil
}

// getObject reads the full contents of a single object from the bucket.
func getObject(ctx context.Context, s3c *s3.Client, bucket, key string) ([]byte, error) {
	resp, err := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body close
	return io.ReadAll(io.LimitReader(resp.Body, maxPBMMetaSize))
}

// parsePBM decodes a single PBM metadata document and, when it describes a
// completed ("done") backup, builds the corresponding external Backup CR,
// carrying the import's classRef/storageRef and a deterministic name so
// re-running discovery never duplicates. It returns only successfully
// completed backups.
func parsePBM(imp *backupv1alpha1.BackupImport, key string, data []byte) (*backupv1alpha1.Backup, error) {
	var meta pbmBackupMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal PBM metadata: %w", err)
	}
	if meta.Status != pbmStatusDone {
		return nil, nil
	}

	path := strings.TrimSuffix(key, pbmMetadataSuffix)

	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      importBackupName(imp.Spec.StorageRef.Name, path),
			Namespace: imp.Namespace,
		},
		Spec: backupv1alpha1.BackupSpec{
			Origin: backupv1alpha1.BackupOrigin{
				Type: backupv1alpha1.BackupOriginTypeExternal,
				External: &backupv1alpha1.BackupOriginExternal{
					Path:        path,
					StartedAt:   metav1.Unix(meta.StartTS, 0),
					CompletedAt: metav1.Unix(meta.LastTransitionTS, 0),
				},
			},
			ClassRef:       imp.Spec.ClassRef,
			StorageRef:     imp.Spec.StorageRef,
			DeletionPolicy: backupv1alpha1.BackupDeletionPolicyRetain,
		},
	}

	return backup, nil
}

// importBackupName derives a deterministic Backup CR name from the (storage,
// path) pair so re-running discovery never creates duplicates. The name is
// "import-" followed by a 64-bit FNV-1a hash rendered as 16 hex digits, which
// is stable, DNS-safe, and collision-resistant for practical bucket sizes.
func importBackupName(storageName, path string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(storageName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	return fmt.Sprintf("import-%016x", h.Sum64())
}
