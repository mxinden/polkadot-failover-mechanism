package test

// This file contains all the supplementary functions that are required to query Load Balancer API V2

import (
	"testing"

	taws "github.com/gruntwork-io/terratest/modules/aws"
    "github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/stretchr/testify/require"
)

// External function that returns a map of target groups and their health statuses
func GetHealthStatusSliceByLBsARN(t *testing.T, awsRegion string, arn string) (map[string]string) {
	result := make(map[string]string)

	TGSSlice := GetTGsbyLBsARN(t, awsRegion, arn)
	for _, tg := range TGSSlice.TargetGroups {
		TGHealth := GetHealthStatusOfTG(t, awsRegion, tg.TargetGroupArn)

		for _, instance := range TGHealth.TargetHealthDescriptions {
			result[*tg.TargetGroupArn] = *instance.TargetHealth.State
		}
	}

	return result
}

// Function that recieves health status of the given target group
func GetHealthStatusOfTG(t *testing.T, awsRegion string, tg *string) *elbv2.DescribeTargetHealthOutput {
        rules, err := GetHealthStatusOfTGE(t, awsRegion, tg)
        require.NoError(t, err)
        return rules
}

func GetHealthStatusOfTGE(t *testing.T, awsRegion string, tg *string) (*elbv2.DescribeTargetHealthOutput, error) {
	nlb := NewNLBClient(t, awsRegion)

	var input = &elbv2.DescribeTargetHealthInput {
			TargetGroupArn: tg,
	}

	return nlb.DescribeTargetHealth(input)
}

// Function that receives all the target groups for the given load balancer
func GetTGsbyLBsARN(t *testing.T, awsRegion string, arn string) *elbv2.DescribeTargetGroupsOutput {
	rules, err := GetTGsbyLBsARNE(t, awsRegion, arn)
        require.NoError(t, err)
        return rules
}

func GetTGsbyLBsARNE(t *testing.T, awsRegion string, arn string) (*elbv2.DescribeTargetGroupsOutput, error) {
	nlb := NewNLBClient(t, awsRegion)

	var input = &elbv2.DescribeTargetGroupsInput {
		LoadBalancerArn: &arn,
	}
	return nlb.DescribeTargetGroups(input)
}

// NewSsmClient creates a SSM client.
func NewNLBClient(t *testing.T, region string) *elbv2.ELBV2 {
        client, err := NewNLBClientE(t, region)
        require.NoError(t, err)
        return client
}

func NewNLBClientE(t *testing.T, region string) (*elbv2.ELBV2, error) {
        sess, err := taws.NewAuthenticatedSession(region)
        if err != nil {
                return nil, err
        }

        return elbv2.New(sess), nil
}
