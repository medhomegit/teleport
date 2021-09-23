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
	"fmt"
	"strings"

	"github.com/gravitational/teleport/api/types"

	"github.com/sirupsen/logrus"
)

// Selector represents a single resource monitor selector.
type Selector struct {
	// MatchLabels is a selector that matches labels.
	MatchLabels types.Labels
	// MatchRDS is a selector that matches RDS databases.
	MatchRDS RDSMatcher
	// MatchRedshift is a selector that matches Redshift databases.
	MatchRedshift RedshiftMatcher
}

// RDSMatcher is a selector that matches RDS databases.
type RDSMatcher struct {
	// Regions are regions to query databases in.
	Regions []string
	// Tags are RDS resource tags to match.
	Tags types.Labels
}

// RedshiftMatcher is a selector that matches Redshift databases.
type RedshiftMatcher struct {
	// Regions are regions to query databases in.
	Regions []string
	// Tags are Redshift resource tags to match.
	Tags types.Labels
}

// String returns the selector string representation.
func (s Selector) String() string {
	var parts []string
	if len(s.MatchLabels) != 0 {
		parts = append(parts, fmt.Sprintf("MatchLabels(%v)", s.MatchLabels))
	}
	if len(s.MatchRDS.Tags) != 0 {
		parts = append(parts, fmt.Sprintf("MatchRDS(%v)", s.MatchRDS.Tags))
	}
	if len(s.MatchRedshift.Tags) != 0 {
		parts = append(parts, fmt.Sprintf("MatchRedshift(%v)", s.MatchRedshift.Tags))
	}
	return strings.Join(parts, ", ")
}

// MatchResourceLabels returns true if any of the provided selectors matches the provided database.
func MatchResourceLabels(selectors []Selector, resource types.ResourceWithLabels) bool {
	for _, selector := range selectors {
		if len(selector.MatchLabels) == 0 {
			return false
		}
		match, _, err := MatchLabels(selector.MatchLabels, resource.GetAllLabels())
		if err != nil {
			logrus.WithError(err).Errorf("Failed to match labels %v: %v.",
				selector.MatchLabels, resource)
			return false
		}
		if match {
			return true
		}
	}
	return false
}
