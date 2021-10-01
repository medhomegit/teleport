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

package cloud

import (
	"context"
	"testing"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/srv/db/common"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/rds"
	"github.com/stretchr/testify/require"
)

// TestRDSIAM tests RDS and Aurora IAM auto-configuration.
func TestRDSIAM(t *testing.T) {
	ctx := context.Background()

	// Setup RDS and Aurora AWS objects.
	rdsInstance := &rds.DBInstance{
		DBInstanceArn:        aws.String("arn:aws:rds:us-west-1:1234567890:db:postgres-rds"),
		DBInstanceIdentifier: aws.String("postgres-rds"),
		DbiResourceId:        aws.String("db-xyz"),
	}

	auroraCluster := &rds.DBCluster{
		DBClusterArn:        aws.String("arn:aws:rds:us-east-1:1234567890:cluster:postgres-aurora"),
		DBClusterIdentifier: aws.String("postgres-aurora"),
		DbClusterResourceId: aws.String("cluster-xyz"),
	}

	// Configure mocks.
	stsClient := &STSMock{
		arn: "arn:aws:iam::1234567890:role/test-role",
	}

	rdsClient := &RDSMock{
		dbInstances: []*rds.DBInstance{rdsInstance},
		dbClusters:  []*rds.DBCluster{auroraCluster},
	}

	iamClient := &IAMMock{}

	// Setup an RDS and Aurora databases.
	rdsDatabase, err := types.NewDatabaseV3(types.Metadata{
		Name: "postgres-rds",
	}, types.DatabaseSpecV3{
		Protocol: defaults.ProtocolPostgres,
		URI:      "localhost",
		AWS:      types.AWS{RDS: types.RDS{InstanceID: "postgres-rds"}},
	})
	require.NoError(t, err)

	auroraDatabase, err := types.NewDatabaseV3(types.Metadata{
		Name: "postgres-aurora",
	}, types.DatabaseSpecV3{
		Protocol: defaults.ProtocolPostgres,
		URI:      "localhost",
		AWS:      types.AWS{RDS: types.RDS{ClusterID: "postgres-aurora"}},
	})
	require.NoError(t, err)

	// Make configurator.
	configurator, err := NewIAM(ctx, IAMConfig{
		Clients: &common.TestCloudClients{
			RDS: rdsClient,
			STS: stsClient,
			IAM: iamClient,
		},
	})
	require.NoError(t, err)

	// Configure RDS database and make sure IAM was enabled and policy was attached.
	err = configurator.Setup(ctx, rdsDatabase)
	require.NoError(t, err)
	require.True(t, aws.BoolValue(rdsInstance.IAMDatabaseAuthenticationEnabled))
	require.Equal(t, []string{"postgres-rds"}, iamClient.attachedRolePolicies["test-role"])

	// Deconfigure RDS database, policy should get detached.
	err = configurator.Teardown(ctx, rdsDatabase)
	require.NoError(t, err)
	require.Equal(t, []string{}, iamClient.attachedRolePolicies["test-role"])

	// Configure Aurora database and make sure IAM was enabled and policy was attached.
	err = configurator.Setup(ctx, auroraDatabase)
	require.NoError(t, err)
	require.True(t, aws.BoolValue(auroraCluster.IAMDatabaseAuthenticationEnabled))
	require.Equal(t, []string{"postgres-aurora"}, iamClient.attachedRolePolicies["test-role"])

	// Deconfigure Aurora database, policy should get detached.
	err = configurator.Teardown(ctx, auroraDatabase)
	require.NoError(t, err)
	require.Equal(t, []string{}, iamClient.attachedRolePolicies["test-role"])
}
