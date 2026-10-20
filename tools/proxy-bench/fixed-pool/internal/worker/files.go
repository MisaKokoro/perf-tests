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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
)

// Publish never overwrites an existing result, even if another process won the race.
func Publish(directory, name string, value any) (err error) {
	raw, err := contract.Encode(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, ".result-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() { err = errors.Join(err, os.Remove(temp)) }()
	_, writeErr := f.Write(raw)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = os.Link(temp, filepath.Join(directory, name)); err != nil {
		return err
	}
	return contract.SyncDir(directory)
}
func Emit(w io.Writer, kind string, value any) error {
	raw, err := contract.Encode(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s%s_V1 %s", Prefix, kind, raw)
	return err
}
func ReadRecord(name string, value any) error {
	raw, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return contract.DecodeRecord(raw, value)
}
func ReadSpec(directory string) (RunSpec, Spec, error) {
	var run RunSpec
	var spec Spec
	if err := ReadRecord(filepath.Join(directory, "run-spec.json"), &run); err != nil {
		return run, spec, err
	}
	err := ReadRecord(filepath.Join(directory, "worker-spec.json"), &spec)
	if err == nil && (run.SchemaVersion != 1 || spec.SchemaVersion != 1 || run.RunID != spec.RunID || spec.WorkerID >= run.WorkerCount || run.Plan.SHA256 != spec.PlanID || run.Manifest.SHA256 != spec.ManifestID) {
		err = fmt.Errorf("run/worker configuration mismatch")
	}
	return run, spec, err
}
func ExitCode(issues []Issue) int {
	result := 0
	for _, issue := range issues {
		code := 0
		switch issue.Code {
		case "INTERRUPTED":
			return 130
		case contract.InputInvalid, contract.ContractMismatch, contract.BudgetExceeded, contract.DataConflict:
			code = 2
		case "RESULT_INCOMPLETE":
			code = 4
		case "COVERAGE_UNVERIFIED", "NO_SAMPLES":
			continue
		default:
			code = 3
		}
		if result == 0 || code == 2 || code == 3 && result == 4 {
			result = code
		}
	}
	return result
}
