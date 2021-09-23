/*
Copyright 2021 Gravitational, Inc.

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

package services

import (
	"context"
	"testing"

	"github.com/gravitational/teleport/api/types"

	"github.com/stretchr/testify/require"
)

// TestReconciler makes sure appropriate callbacks are called during reconciliation.
func TestReconciler(t *testing.T) {
	tests := []struct {
		description         string
		selectors           []Selector
		registeredResources types.ResourcesWithLabels
		newResources        types.ResourcesWithLabels
		onCreateCalls       types.ResourcesWithLabels
		onUpdateCalls       types.ResourcesWithLabels
		onDeleteCalls       types.ResourcesWithLabels
	}{
		{
			description: "new matching resource should be registered",
			selectors: []Selector{{
				MatchLabels: types.Labels{"*": []string{"*"}},
			}},
			registeredResources: types.ResourcesWithLabels{},
			newResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", nil),
			},
			onCreateCalls: types.ResourcesWithLabels{
				makeDynamicResource("res1", nil),
			},
		},
		{
			description: "new non-matching resource should not be registered",
			selectors: []Selector{{
				MatchLabels: types.Labels{"env": []string{"prod"}},
			}},
			registeredResources: types.ResourcesWithLabels{},
			newResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "dev"}),
			},
		},
		{
			description: "resources with different origins don't overwrite each other",
			selectors: []Selector{{
				MatchLabels: types.Labels{"*": []string{"*"}},
			}},
			registeredResources: types.ResourcesWithLabels{
				makeStaticResource("res1", nil),
			},
			newResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", nil),
			},
		},
		{
			description: "resource that's no longer present should be removed",
			selectors: []Selector{{
				MatchLabels: types.Labels{"*": []string{"*"}},
			}},
			registeredResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", nil),
			},
			newResources: types.ResourcesWithLabels{},
			onDeleteCalls: types.ResourcesWithLabels{
				makeDynamicResource("res1", nil),
			},
		},
		{
			description: "resource with updated matching labels should be updated",
			selectors: []Selector{{
				MatchLabels: types.Labels{"*": []string{"*"}},
			}},
			registeredResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", nil),
			},
			newResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "dev"}),
			},
			onUpdateCalls: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "dev"}),
			},
		},
		{
			description: "non-matching updated resource should be removed",
			selectors: []Selector{{
				MatchLabels: types.Labels{"env": []string{"prod"}},
			}},
			registeredResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "prod"}),
			},
			newResources: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "dev"}),
			},
			onDeleteCalls: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "prod"}),
			},
		},
		{
			description: "complex scenario with multiple created/updated/deleted resources",
			selectors: []Selector{{
				MatchLabels: types.Labels{"env": []string{"prod"}},
			}},
			registeredResources: types.ResourcesWithLabels{
				makeStaticResource("res0", nil),
				makeDynamicResource("res1", map[string]string{"env": "prod"}),
				makeDynamicResource("res2", map[string]string{"env": "prod"}),
				makeDynamicResource("res3", map[string]string{"env": "prod"}),
				makeDynamicResource("res4", map[string]string{"env": "prod"}),
			},
			newResources: types.ResourcesWithLabels{
				makeDynamicResource("res0", map[string]string{"env": "prod"}),
				makeDynamicResource("res2", map[string]string{"env": "prod", "a": "b"}),
				makeDynamicResource("res3", map[string]string{"env": "prod"}),
				makeDynamicResource("res4", map[string]string{"env": "dev"}),
				makeDynamicResource("res5", map[string]string{"env": "prod"}),
				makeDynamicResource("res6", map[string]string{"env": "dev"}),
			},
			onCreateCalls: types.ResourcesWithLabels{
				makeDynamicResource("res5", map[string]string{"env": "prod"}),
			},
			onUpdateCalls: types.ResourcesWithLabels{
				makeDynamicResource("res2", map[string]string{"env": "prod", "a": "b"}),
			},
			onDeleteCalls: types.ResourcesWithLabels{
				makeDynamicResource("res1", map[string]string{"env": "prod"}),
				makeDynamicResource("res4", map[string]string{"env": "prod"}),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			// Reconciler will record all callback calls in these lists.
			var onCreateCalls, onUpdateCalls, onDeleteCalls types.ResourcesWithLabels

			reconciler, err := NewReconciler(ReconcilerConfig{
				Matcher: func(rwl types.ResourceWithLabels) bool {
					return MatchResourceLabels(test.selectors, rwl)
				},
				GetResources: func() types.ResourcesWithLabels {
					return test.registeredResources
				},
				OnCreate: func(ctx context.Context, r types.ResourceWithLabels) error {
					onCreateCalls = append(onCreateCalls, r)
					return nil
				},
				OnUpdate: func(ctx context.Context, r types.ResourceWithLabels) error {
					onUpdateCalls = append(onUpdateCalls, r)
					return nil
				},
				OnDelete: func(ctx context.Context, r types.ResourceWithLabels) error {
					onDeleteCalls = append(onDeleteCalls, r)
					return nil
				},
			})
			require.NoError(t, err)

			// Reconcile and make sure we got all expected callback calls.
			err = reconciler.Reconcile(context.Background(), test.newResources)
			require.NoError(t, err)
			require.Equal(t, test.onCreateCalls, onCreateCalls)
			require.Equal(t, test.onUpdateCalls, onUpdateCalls)
			require.Equal(t, test.onDeleteCalls, onDeleteCalls)
		})
	}
}

func makeStaticResource(name string, labels map[string]string) types.ResourceWithLabels {
	return makeResource(name, labels, map[string]string{
		types.OriginLabel: types.OriginConfigFile,
	})
}

func makeDynamicResource(name string, labels map[string]string) types.ResourceWithLabels {
	return makeResource(name, labels, map[string]string{
		types.OriginLabel: types.OriginDynamic,
	})
}

func makeResource(name string, labels map[string]string, additionalLabels map[string]string) types.ResourceWithLabels {
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range additionalLabels {
		labels[k] = v
	}
	return &testResource{
		Metadata: types.Metadata{
			Name:   name,
			Labels: labels,
		},
	}
}

type testResource struct {
	types.ResourceWithLabels
	Metadata types.Metadata
}

func (r *testResource) GetName() string {
	return r.Metadata.Name
}

func (r *testResource) Origin() string {
	return r.Metadata.Labels[types.OriginLabel]
}

func (r *testResource) GetAllLabels() map[string]string {
	return r.Metadata.Labels
}
