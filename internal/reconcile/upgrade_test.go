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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fluxcd/pkg/runtime/conditions"
	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	helmchart "helm.sh/helm/v3/pkg/chart"
	helmchartutil "helm.sh/helm/v3/pkg/chartutil"
	helmrelease "helm.sh/helm/v3/pkg/release"
	helmreleaseutil "helm.sh/helm/v3/pkg/releaseutil"
	helmstorage "helm.sh/helm/v3/pkg/storage"
	helmdriver "helm.sh/helm/v3/pkg/storage/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta2"
	"github.com/fluxcd/helm-controller/internal/action"
	"github.com/fluxcd/helm-controller/internal/release"
	"github.com/fluxcd/helm-controller/internal/storage"
	"github.com/fluxcd/helm-controller/internal/testutil"
)

func Test_upgrade(t *testing.T) {
	var (
		mockCreateErr = fmt.Errorf("storage create error")
		mockUpdateErr = fmt.Errorf("storage update error")
	)

	tests := []struct {
		name string
		// driver allows for modifying the Helm storage driver.
		driver func(driver helmdriver.Driver) helmdriver.Driver
		// releases is the list of releases that are stored in the driver
		// before upgrade.
		releases func(namespace string) []*helmrelease.Release
		// chart to upgrade.
		chart *helmchart.Chart
		// values to use during upgrade.
		values helmchartutil.Values
		// spec modifies the HelmRelease object spec before upgrade.
		spec func(spec *helmv2.HelmReleaseSpec)
		// status to configure on the HelmRelease Object before upgrade.
		status func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus
		// wantErr is the error that is expected to be returned.
		wantErr error
		// expectedConditions are the conditions that are expected to be set on
		// the HelmRelease after upgrade.
		expectConditions []metav1.Condition
		// expectCurrent is the expected Current release information in the
		// HelmRelease after upgrade.
		expectCurrent func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo
		// expectPrevious returns the expected Previous release information of
		// the HelmRelease after upgrade.
		expectPrevious func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo
		// expectFailures is the expected Failures count of the HelmRelease.
		expectFailures int64
		// expectInstallFailures is the expected InstallFailures count of the
		// HelmRelease.
		expectInstallFailures int64
		// expectUpgradeFailures is the expected UpgradeFailures count of the
		// HelmRelease.
		expectUpgradeFailures int64
	}{
		{
			name: "upgrade success",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(testutil.ChartWithTestHook()),
						Version:   1,
						Status:    helmrelease.StatusDeployed,
					}),
				}
			},
			chart: testutil.BuildChart(),
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: release.ObservedToInfo(release.ObserveRelease(releases[0])),
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.TrueCondition(helmv2.ReleasedCondition, helmv2.UpgradeSucceededReason,
					"Upgrade complete"),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[1]))
			},
			expectPrevious: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[0]))
			},
		},
		{
			name: "upgrade failure",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(),
						Version:   1,
						Status:    helmrelease.StatusDeployed,
					}),
				}
			},
			chart: testutil.BuildChart(testutil.ChartWithFailingHook()),
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: release.ObservedToInfo(release.ObserveRelease(releases[0])),
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.FalseCondition(helmv2.ReleasedCondition, helmv2.UpgradeFailedReason,
					"post-upgrade hooks failed: 1 error occurred:\n\t* timed out waiting for the condition\n\n"),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[1]))
			},
			expectPrevious: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[0]))
			},
			expectFailures:        1,
			expectUpgradeFailures: 1,
		},
		{
			name: "upgrade failure without storage create",
			driver: func(driver helmdriver.Driver) helmdriver.Driver {
				return &storage.Failing{
					Driver:    driver,
					CreateErr: mockCreateErr,
				}
			},
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(),
						Version:   1,
						Status:    helmrelease.StatusDeployed,
					}),
				}
			},
			chart: testutil.BuildChart(),
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: release.ObservedToInfo(release.ObserveRelease(releases[0])),
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.FalseCondition(helmv2.ReleasedCondition, helmv2.UpgradeFailedReason,
					mockCreateErr.Error()),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[0]))
			},
			expectFailures:        1,
			expectUpgradeFailures: 1,
		},
		{
			name: "upgrade failure without storage update",
			driver: func(driver helmdriver.Driver) helmdriver.Driver {
				return &storage.Failing{
					Driver:    driver,
					UpdateErr: mockUpdateErr,
				}
			},
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(),
						Version:   1,
						Status:    helmrelease.StatusDeployed,
					}),
				}
			},
			chart: testutil.BuildChart(),
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: release.ObservedToInfo(release.ObserveRelease(releases[0])),
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.FalseCondition(helmv2.ReleasedCondition, helmv2.UpgradeFailedReason,
					mockUpdateErr.Error()),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[1]))
			},
			expectPrevious: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[0]))
			},
			expectFailures:        1,
			expectUpgradeFailures: 1,
		},
		{
			name: "upgrade without current",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(),
						Version:   1,
						Status:    helmrelease.StatusDeployed,
					}),
				}
			},
			chart: testutil.BuildChart(),
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: nil,
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.TrueCondition(helmv2.ReleasedCondition, helmv2.UpgradeSucceededReason,
					"Upgrade complete"),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[1]))
			},
		},
		{
			name: "upgrade with stale current",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(),
						Version:   1,
						Status:    helmrelease.StatusSuperseded,
					}),
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Namespace: namespace,
						Chart:     testutil.BuildChart(),
						Version:   2,
						Status:    helmrelease.StatusDeployed,
					}),
				}
			},
			chart: testutil.BuildChart(),
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: &helmv2.HelmReleaseInfo{
						Name:      mockReleaseName,
						Namespace: releases[0].Namespace,
						Version:   1,
						Status:    helmrelease.StatusDeployed.String(),
					},
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.TrueCondition(helmv2.ReleasedCondition, helmv2.UpgradeSucceededReason,
					"Upgrade complete"),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[2]))
			},
			expectPrevious: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return &helmv2.HelmReleaseInfo{
					Name:      mockReleaseName,
					Namespace: releases[0].Namespace,
					Version:   1,
					Status:    helmrelease.StatusDeployed.String(),
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			namedNS, err := testEnv.CreateNamespace(context.TODO(), mockReleaseNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			t.Cleanup(func() {
				_ = testEnv.Delete(context.TODO(), namedNS)
			})
			releaseNamespace := namedNS.Name

			var releases []*helmrelease.Release
			if tt.releases != nil {
				releases = tt.releases(releaseNamespace)
				helmreleaseutil.SortByRevision(releases)
			}

			obj := &helmv2.HelmRelease{
				Spec: helmv2.HelmReleaseSpec{
					ReleaseName:      mockReleaseName,
					TargetNamespace:  releaseNamespace,
					StorageNamespace: releaseNamespace,
					Timeout:          &metav1.Duration{Duration: 100 * time.Millisecond},
				},
			}
			if tt.spec != nil {
				tt.spec(&obj.Spec)
			}
			if tt.status != nil {
				obj.Status = tt.status(releases)
			}

			getter, err := RESTClientGetterFromManager(testEnv.Manager, obj.GetReleaseNamespace())
			g.Expect(err).ToNot(HaveOccurred())

			cfg, err := action.NewConfigFactory(getter,
				action.WithStorage(action.DefaultStorageDriver, obj.GetStorageNamespace()),
				action.WithDebugLog(logr.Discard()),
			)
			g.Expect(err).ToNot(HaveOccurred())

			store := helmstorage.Init(cfg.Driver)
			for _, r := range releases {
				g.Expect(store.Create(r)).To(Succeed())
			}

			if tt.driver != nil {
				cfg.Driver = tt.driver(cfg.Driver)
			}

			got := (&Upgrade{configFactory: cfg}).Reconcile(context.TODO(), &Request{
				Object: obj,
				Chart:  tt.chart,
				Values: tt.values,
			})
			if tt.wantErr != nil {
				g.Expect(got).To(Equal(tt.wantErr))
			} else {
				g.Expect(got).ToNot(HaveOccurred())
			}

			g.Expect(obj.Status.Conditions).To(conditions.MatchConditions(tt.expectConditions))

			releases, _ = store.History(mockReleaseName)
			helmreleaseutil.SortByRevision(releases)

			if tt.expectCurrent != nil {
				g.Expect(obj.Status.Current).To(testutil.Equal(tt.expectCurrent(releases)))
			} else {
				g.Expect(obj.Status.Current).To(BeNil(), "expected current to be nil")
			}

			if tt.expectPrevious != nil {
				g.Expect(obj.Status.Previous).To(testutil.Equal(tt.expectPrevious(releases)))
			} else {
				g.Expect(obj.Status.Previous).To(BeNil(), "expected previous to be nil")
			}

			g.Expect(obj.Status.Failures).To(Equal(tt.expectFailures))
			g.Expect(obj.Status.InstallFailures).To(Equal(tt.expectInstallFailures))
			g.Expect(obj.Status.UpgradeFailures).To(Equal(tt.expectUpgradeFailures))
		})
	}
}