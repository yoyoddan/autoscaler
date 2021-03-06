/*
Copyright 2018 The Kubernetes Authors.

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

package api

import (
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1beta2"
	vpa_fake "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/fake"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

const (
	containerName = "container1"
)

var (
	anytime = time.Unix(0, 0)
)

func init() {
	flag.Set("alsologtostderr", "true")
	flag.Set("v", "5")
}

func parseLabelSelector(selector string) labels.Selector {
	labelSelector, _ := meta.ParseToLabelSelector(selector)
	parsedSelector, _ := meta.LabelSelectorAsSelector(labelSelector)
	return parsedSelector
}

func TestUpdateVpaIfNeeded(t *testing.T) {
	modelVpa := model.NewVpa(model.VpaID{VpaName: "vpa", Namespace: "test"}, nil, time.Now())
	modelVpa.Conditions.Set(vpa_types.RecommendationProvided, true, "reason", "msg")
	condition := modelVpa.Conditions[vpa_types.RecommendationProvided]
	condition.LastTransitionTime = meta.NewTime(anytime)
	modelVpa.Conditions[vpa_types.RecommendationProvided] = condition
	recommendation := test.Recommendation().WithContainer(containerName).WithTarget("5", "200").Get()
	observedVpaBuilder := test.VerticalPodAutoscaler().WithName("vpa").WithNamespace("test").WithContainer(containerName)
	modelVpa.Recommendation = recommendation
	testCases := []struct {
		caseName       string
		vpa            *model.Vpa
		observedStatus *vpa_types.VerticalPodAutoscalerStatus
		expectedUpdate bool
	}{
		{
			caseName: "Doesn't update if no changes.",
			vpa:      modelVpa,
			observedStatus: &observedVpaBuilder.WithTarget("5", "200").
				AppendCondition(vpa_types.RecommendationProvided, core.ConditionTrue, "reason", "msg", anytime).Get().Status,
			expectedUpdate: false,
		}, {
			caseName: "Updates on recommendation change.",
			vpa:      modelVpa,
			observedStatus: &observedVpaBuilder.WithTarget("10", "200").
				AppendCondition(vpa_types.RecommendationProvided, core.ConditionTrue, "reason", "msg", anytime).Get().Status,
			expectedUpdate: true,
		}, {
			caseName: "Updates on condition change.",
			vpa:      modelVpa,
			observedStatus: &observedVpaBuilder.WithTarget("5", "200").
				AppendCondition(vpa_types.RecommendationProvided, core.ConditionFalse, "reason", "msg", anytime).Get().Status,
			expectedUpdate: true,
		}, {
			caseName: "Updates on condition added.",
			vpa:      modelVpa,
			observedStatus: &observedVpaBuilder.WithTarget("5", "200").
				AppendCondition(vpa_types.RecommendationProvided, core.ConditionTrue, "reason", "msg", anytime).
				AppendCondition(vpa_types.LowConfidence, core.ConditionTrue, "reason", "msg", anytime).Get().Status,
			expectedUpdate: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.caseName, func(t *testing.T) {
			fakeClient := vpa_fake.NewSimpleClientset()
			_, err := UpdateVpaStatusIfNeeded(fakeClient.AutoscalingV1beta2().VerticalPodAutoscalers(tc.vpa.ID.Namespace),
				tc.vpa, tc.observedStatus)
			assert.NoError(t, err, "Unexpected error occurred.")
			actions := fakeClient.Actions()
			if tc.expectedUpdate {
				assert.Equal(t, 1, len(actions), "Unexpected number of actions")
			} else {
				assert.Equal(t, 0, len(actions), "Unexpected number of actions")
			}
		})
	}
}

func TestPodMatchesVPA(t *testing.T) {
	type testCase struct {
		pod             *core.Pod
		vpaWithSelector VpaWithSelector
		result          bool
	}

	pod := test.Pod().WithName("test-pod").AddContainer(test.BuildTestContainer(containerName, "1", "100M")).Get()
	pod.Labels = map[string]string{"app": "testingApp"}

	vpaBuilder := test.VerticalPodAutoscaler().
		WithContainer(containerName).
		WithTarget("2", "200M").
		WithMinAllowed("1", "100M").
		WithMaxAllowed("3", "1G")

	vpa := vpaBuilder.Get()
	otherNamespaceVPA := vpaBuilder.WithNamespace("other").Get()

	testCases := []testCase{
		{pod, VpaWithSelector{vpa, parseLabelSelector("app = testingApp")}, true},
		{pod, VpaWithSelector{otherNamespaceVPA, parseLabelSelector("app = testingApp")}, false},
		{pod, VpaWithSelector{vpa, parseLabelSelector("app = other")}, false}}

	for _, tc := range testCases {
		actual := PodMatchesVPA(tc.pod, &tc.vpaWithSelector)
		assert.Equal(t, tc.result, actual)
	}
}

func TestGetControllingVPAForPod(t *testing.T) {
	pod := test.Pod().WithName("test-pod").AddContainer(test.BuildTestContainer(containerName, "1", "100M")).Get()
	pod.Labels = map[string]string{"app": "testingApp"}

	vpaBuilder := test.VerticalPodAutoscaler().
		WithContainer(containerName).
		WithTarget("2", "200M").
		WithMinAllowed("1", "100M").
		WithMaxAllowed("3", "1G")
	vpaA := vpaBuilder.WithCreationTimestamp(time.Unix(5, 0)).Get()
	vpaB := vpaBuilder.WithCreationTimestamp(time.Unix(10, 0)).Get()
	nonMatchingVPA := vpaBuilder.WithCreationTimestamp(time.Unix(2, 0)).Get()

	chosen := GetControllingVPAForPod(pod, []*VpaWithSelector{
		{vpaB, parseLabelSelector("app = testingApp")},
		{vpaA, parseLabelSelector("app = testingApp")},
		{nonMatchingVPA, parseLabelSelector("app = other")},
	})
	assert.Equal(t, vpaA, chosen.Vpa)
}

func TestGetContainerResourcePolicy(t *testing.T) {
	containerPolicy1 := vpa_types.ContainerResourcePolicy{
		ContainerName: "container1",
		MinAllowed: core.ResourceList{
			core.ResourceCPU: *resource.NewScaledQuantity(10, 1),
		},
	}
	containerPolicy2 := vpa_types.ContainerResourcePolicy{
		ContainerName: "container2",
		MaxAllowed: core.ResourceList{
			core.ResourceMemory: *resource.NewScaledQuantity(100, 1),
		},
	}
	policy := vpa_types.PodResourcePolicy{
		ContainerPolicies: []vpa_types.ContainerResourcePolicy{
			containerPolicy1, containerPolicy2,
		},
	}
	assert.Equal(t, &containerPolicy1, GetContainerResourcePolicy("container1", &policy))
	assert.Equal(t, &containerPolicy2, GetContainerResourcePolicy("container2", &policy))
	assert.Nil(t, GetContainerResourcePolicy("container3", &policy))

	// Add the wildcard ("*") policy.
	defaultPolicy := vpa_types.ContainerResourcePolicy{
		ContainerName: "*",
		MinAllowed: core.ResourceList{
			core.ResourceCPU: *resource.NewScaledQuantity(20, 1),
		},
	}
	policy = vpa_types.PodResourcePolicy{
		ContainerPolicies: []vpa_types.ContainerResourcePolicy{
			containerPolicy1, defaultPolicy, containerPolicy2,
		},
	}
	assert.Equal(t, &containerPolicy1, GetContainerResourcePolicy("container1", &policy))
	assert.Equal(t, &containerPolicy2, GetContainerResourcePolicy("container2", &policy))
	assert.Equal(t, &defaultPolicy, GetContainerResourcePolicy("container3", &policy))
}
