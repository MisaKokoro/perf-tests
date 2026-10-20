/*
 *     Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package worker

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/target"
)

// CheckPreparation checks saved evidence without contacting either Seed or origin.
func CheckPreparation(raw []byte, name string, m contract.Manifest, manifestID string, c target.Config, identity string) error {
	var r target.Result
	if err := contract.DecodeRecord(raw, &r); err != nil {
		return fmt.Errorf("TARGET_PREPARATION: %w", err)
	}
	if r.SchemaVersion != 1 || r.PreparationID == "" || r.Status != target.StatusReady || len(r.Errors) != 0 {
		return fmt.Errorf("target-preparation status/schema/errors is not ready")
	}
	if r.ManifestSHA256 == nil || *r.ManifestSHA256 != manifestID {
		return fmt.Errorf("target-preparation manifest_sha256 mismatch")
	}
	if r.IdentitySHA256 == nil || *r.IdentitySHA256 != identity || r.PieceStrategy != "auto" || r.Method != target.Method {
		return fmt.Errorf("target-preparation identity_sha256/piece_strategy/method mismatch")
	}
	if r.Target == nil || *r.Target != c.Target || r.ClientVersion == nil || *r.ClientVersion != c.Target.ClientVersion {
		return fmt.Errorf("target-preparation target/client_version mismatch")
	}
	if r.ObjectCount == nil || *r.ObjectCount != m.ObjectCount || r.Prepare == nil || r.Prepare.Concurrency == 0 || r.Prepare.RequestTimeoutMS == 0 || r.Prepare.ReadyTimeoutMS == 0 {
		return fmt.Errorf("target-preparation object_count/prepare invalid")
	}
	start, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err != nil {
		return fmt.Errorf("target-preparation started_at invalid")
	}
	end, err := time.Parse(time.RFC3339Nano, r.EndedAt)
	if err != nil || end.Before(start) {
		return fmt.Errorf("target-preparation ended_at invalid")
	}
	if r.EvidenceFile == nil || r.EvidenceFile.Path != "target-object-results.json" || !contract.ValidDigest(r.EvidenceFile.SHA256) {
		return fmt.Errorf("target-preparation evidence_file invalid")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(name), r.EvidenceFile.Path))
	if err != nil {
		return fmt.Errorf("evidence_file: %w", err)
	}
	if contract.Digest(data) != r.EvidenceFile.SHA256 {
		return fmt.Errorf("evidence_file SHA-256 mismatch")
	}
	var results target.ObjectResults
	if err = contract.DecodeRecord(data, &results); err != nil {
		return fmt.Errorf("target-object-results: %w", err)
	}
	if results.SchemaVersion != 1 || len(results.Objects) != len(m.Objects) {
		return fmt.Errorf("target-object-results schema/count invalid")
	}
	seen := map[string]bool{}
	for i, o := range results.Objects {
		expected := m.Objects[i]
		if o.ObjectID != expected.ID || o.Status != target.StatusReady || o.Error != nil || o.TaskID == nil || strings.TrimSpace(*o.TaskID) == "" || seen[*o.TaskID] || o.ServerIP == nil || !net.ParseIP(*o.ServerIP).Equal(net.ParseIP(c.Target.ExpectedServerIP)) || o.ReceivedBytes == nil || *o.ReceivedBytes != expected.Size || o.SHA256 == nil || *o.SHA256 != expected.SHA256 {
			return fmt.Errorf("target-object-results object %d identity/body/status mismatch", i)
		}
		seen[*o.TaskID] = true
		if o.FullReadCompletedAt == nil || o.FinishedCheckAt == nil {
			return fmt.Errorf("object %d evidence timestamps missing", i)
		}
		full, e1 := time.Parse(time.RFC3339Nano, *o.FullReadCompletedAt)
		finished, e2 := time.Parse(time.RFC3339Nano, *o.FinishedCheckAt)
		if e1 != nil || e2 != nil || full.Before(start) || finished.Before(full) || end.Before(finished) {
			return fmt.Errorf("object %d evidence timestamps invalid", i)
		}
	}
	return nil
}
