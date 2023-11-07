/*
Copyright 2022 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reconcile

import (
	"errors"
	"fmt"

	helmrelease "helm.sh/helm/v3/pkg/release"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fluxcd/helm-controller/internal/action"
	interrors "github.com/fluxcd/helm-controller/internal/errors"
	"github.com/fluxcd/helm-controller/internal/release"
)

// ReleaseStatus represents the status of a Helm release as determined by
// comparing the Helm storage with the v2beta2.HelmRelease object.
type ReleaseStatus string

// String returns the string representation of the release status.
func (s ReleaseStatus) String() string {
	return string(s)
}

const (
	// ReleaseStatusUnknown indicates that the status of the release could not
	// be determined.
	ReleaseStatusUnknown ReleaseStatus = "Unknown"
	// ReleaseStatusAbsent indicates that the release is not present in the
	// Helm storage.
	ReleaseStatusAbsent ReleaseStatus = "Absent"
	// ReleaseStatusOrphaned indicates that the release is present in the Helm
	// storage, but is not managed by the v2beta2.HelmRelease object.
	ReleaseStatusOrphaned ReleaseStatus = "Orphaned"
	// ReleaseStatusOutOfSync indicates that the release is present in the Helm
	// storage, but is not in sync with the v2beta2.HelmRelease object.
	ReleaseStatusOutOfSync ReleaseStatus = "OutOfSync"
	// ReleaseStatusLocked indicates that the release is present in the Helm
	// storage, but is locked.
	ReleaseStatusLocked ReleaseStatus = "Locked"
	// ReleaseStatusUntested indicates that the release is present in the Helm
	// storage, but has not been tested.
	ReleaseStatusUntested ReleaseStatus = "Untested"
	// ReleaseStatusInSync indicates that the release is present in the Helm
	// storage, and is in sync with the v2beta2.HelmRelease object.
	ReleaseStatusInSync ReleaseStatus = "InSync"
	// ReleaseStatusFailed indicates that the release is present in the Helm
	// storage, but has failed.
	ReleaseStatusFailed ReleaseStatus = "Failed"
)

// ReleaseState represents the state of a Helm release as determined by
// comparing the Helm storage with the v2beta2.HelmRelease object.
type ReleaseState struct {
	// Status is the status of the release.
	Status ReleaseStatus
	// Reason for the Status.
	Reason string
}

// MustResetHistory returns true if the release state indicates that the
// history on the v2beta2.HelmRelease object must be reset.
// This is the case when the release in storage has been mutated in such a way
// that it no longer can be used to roll back to, or perform a diff against.
func (s ReleaseState) MustResetHistory() bool {
	return s.Status == ReleaseStatusLocked || s.Status == ReleaseStatusOrphaned || s.Status == ReleaseStatusAbsent
}

func DetermineReleaseState(cfg *action.ConfigFactory, req *Request) (ReleaseState, error) {
	rls, err := action.LastRelease(cfg.Build(nil), req.Object.GetReleaseName())
	if err != nil {
		if errors.Is(err, action.ErrReleaseNotFound) {
			return ReleaseState{Status: ReleaseStatusAbsent, Reason: "no release in storage for object"}, nil
		}
		return ReleaseState{Status: ReleaseStatusUnknown}, fmt.Errorf("failed to determine last release: %w", err)
	}

	ctrl.Log.Info("DetermineReleaseState", "rls", release.ObserveRelease(rls))

	if rls.Info.Status.IsPending() {
		return ReleaseState{Status: ReleaseStatusLocked, Reason: fmt.Sprintf("release with status '%s'", rls.Info.Status)}, err
	}

	cur := req.Object.GetCurrent()
	if cur == nil {
		if rls.Info.Status == helmrelease.StatusUninstalled {
			return ReleaseState{Status: ReleaseStatusAbsent, Reason: "found uninstalled release in storage"}, nil
		}
		return ReleaseState{Status: ReleaseStatusOrphaned, Reason: "found existing release in storage not managed by object"}, err
	}
	if err := action.VerifyReleaseObject(cur, rls); err != nil {
		if interrors.IsOneOf(err, action.ErrReleaseDigest, action.ErrReleaseNotObserved) {
			return ReleaseState{Status: ReleaseStatusOrphaned, Reason: err.Error()}, nil
		}
		return ReleaseState{Status: ReleaseStatusUnknown}, fmt.Errorf("failed to verify release object against current: %w", err)
	}

	switch rls.Info.Status {
	case helmrelease.StatusFailed:
		return ReleaseState{Status: ReleaseStatusFailed}, nil
	case helmrelease.StatusUninstalled, helmrelease.StatusSuperseded:
		return ReleaseState{Status: ReleaseStatusAbsent, Reason: fmt.Sprintf("release with status '%s'", rls.Info.Status)}, nil
	case helmrelease.StatusDeployed:
		if err = action.VerifyRelease(rls, cur, req.Chart.Metadata, req.Values); err != nil {
			switch err {
			case action.ErrChartChanged, action.ErrConfigDigest:
				return ReleaseState{Status: ReleaseStatusOutOfSync, Reason: err.Error()}, nil
			default:
				return ReleaseState{Status: ReleaseStatusUnknown}, err
			}
		}

		// For the further determination of test results, we look at the
		// observed state of the object. As tests can be run manually by
		// users running e.g. `helm test`.
		if testSpec := req.Object.GetTest(); testSpec.Enable {
			// Confirm the release has been tested if enabled.
			if !req.Object.GetCurrent().HasBeenTested() {
				return ReleaseState{Status: ReleaseStatusUntested}, nil
			}

			// Act on any observed test failure.
			remedation := req.Object.GetActiveRemediation()
			if !remedation.MustIgnoreTestFailures(testSpec.IgnoreFailures) && req.Object.GetCurrent().HasTestInPhase(helmrelease.HookPhaseFailed.String()) {
				return ReleaseState{Status: ReleaseStatusFailed, Reason: "has failed test"}, nil
			}
		}
		return ReleaseState{Status: ReleaseStatusInSync}, nil
	default:
		return ReleaseState{Status: ReleaseStatusUnknown}, fmt.Errorf("unknown release status: %s", rls.Info.Status)
	}
}
