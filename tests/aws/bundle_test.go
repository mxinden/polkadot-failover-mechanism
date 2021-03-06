package test

// Set AWS_ACCESS_KEY, AWS_SECRET_KEY, PREFIX before running these scripts

import (
	"testing"
	"os"
	"time"
	"strconv"
	"fmt"
	"encoding/json"
        "strings"

	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/terraform"
        "github.com/gruntwork-io/terratest/modules/ssh"
	"github.com/gruntwork-io/terratest/modules/retry"

	"github.com/google/go-cmp/cmp"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/stretchr/testify/assert"
)

//Gather environmental variables and set reasonable defaults
var awsRegion = [3]string{"us-east-1", "us-east-2", "us-west-1"}
var aws_access_keys = []string{os.Getenv("AWS_ACCESS_KEY")}
var aws_secret_keys = []string{os.Getenv("AWS_SECRET_KEY")}
var prefix = os.Getenv("PREFIX")

// A collection of tests that will be run
func TestBundle(t *testing.T) {

    // Set backend variables
	var s3bucket, s3key, s3region string
    
	if value, ok := os.LookupEnv("TF_STATE_BUCKET"); ok {
		s3bucket = value
	} else {
		s3bucket = "polkadot-validator-failover-tfstate"
	}

        if value, ok := os.LookupEnv("TF_STATE_KEY"); ok {
		s3key = value
	} else {
		s3key = "terraform.tfstate"
	}

        if value, ok := os.LookupEnv("TF_STATE_REGION"); ok {
		s3region = value
	} else {
		s3region = "us-east-1"
	}

    // Generate new SSH key for test virtual machines
	sshKey := ssh.GenerateRSAKeyPair(t, 4096)

	// Configure Terraform - set backend, minimum set of infrastructure variables. Also expose ssh 
	terraformOptions := &terraform.Options{
		// The path to where our Terraform code is located
		TerraformDir: "../../aws/",

		BackendConfig: map[string]interface{}{
			"bucket": s3bucket,
		    "region": s3region,
			"key": prefix + "-" + s3key,
	        },

		// Variables to pass to our Terraform code using -var options
		Vars: map[string]interface{}{
			"aws_access_keys": aws_access_keys,
			"aws_secret_keys": aws_secret_keys,
			"aws_regions": "[\"" + awsRegion[0] + "\", \"" + awsRegion[1] + "\", \"" + awsRegion[2] + "\"]",
			"validator_keys": "{key1={key=\"0x6ce96ae5c300096b09dbd4567b0574f6a1281ae0e5cfe4f6b0233d1821f6206b\",type=\"gran\",seed=\"favorite liar zebra assume hurt cage any damp inherit rescue delay panic\"},key2={key=\"0x3ff0766f9ebbbceee6c2f40d9323164d07e70c70994c9d00a9512be6680c2394\",type=\"aura\",seed=\"expire stage crawl shell boss any story swamp skull yellow bamboo copy\"}}",
			"key_name": "test",
			"key_content": sshKey.PublicKey,
                        "prefix": prefix,
			"delete_on_termination": "true",
			"cpu_limit": "1",
			"ram_limit": "1",
			"validator_name": "test",
			"expose_ssh": "true",
			"node_key": "fc9c7cf9b4523759b0a43b15ff07064e70b9a2d39ef16c8f62391794469a1c5e",
                        "chain": "westend",
		},
	}

	// At the end of the test, run `terraform destroy` to clean up any resources that were created
	defer terraform.Destroy(t, terraformOptions)

	// Run `terraform init` and `terraform apply` and fail the test if there are any errors
	terraform.InitAndApply(t, terraformOptions)

    // TEST 1: Verify that there are healthy instances in each region with public ips assigned
	var instanceIDs []string
	publicIPs := make(map[string]string)

	for _, value := range awsRegion {
        // GetHealthyEc2InstanceIdsByTag located in ec2.go file
		regionInstances := GetHealthyEc2InstanceIdsByTag(t, value, "prefix", os.Getenv("PREFIX"))

		if len(regionInstances) < 1 {
			t.Error("ERROR! No instances found in " + value + " region.")
		} else {
			t.Log("INFO. The following instances found in " + value + " region: " + strings.Join(regionInstances,",") + ".")
		}

		instanceIDs = append(instanceIDs, regionInstances...)
        // Fetching PublicIPs for the instances we have found
		regionIPs := aws.GetPublicIpsOfEc2Instances(t, regionInstances, value)

		if len(regionIPs) < 1 {
			t.Error("ERROR! No public IPs found for instances in " + value + " region.")
		}

		for k, v := range regionIPs {

			publicIPs[k] = v

		}
	}

	t.Log("INFO. Instances IDs found in all regions: " + strings.Join(instanceIDs,","))
	t.Log("INFO. Instances IPs found in all regions: ")

	for key, value := range publicIPs {
		t.Log("InstanceID: " + key + ", InstanceIP: " + value)
	}

      var test bool = false
    // TEST 2: Veriy the number of existing EC2 instances - should be an odd number
	t.Run("Instance count", func(t *testing.T) {

		instance_count := len(instanceIDs)

		test = assert.Equal(t, instance_count % 2, 1)
		if test {
			t.Log("INFO. There are odd instances running")
		} else {
			t.Error("ERROR! There are even instances running")
		}

    // TEST 3: Verify the number of existing EC2 instances - should be at least 3
		test = assert.True(t, instance_count > 2)
		if test {
			t.Log("INFO. Minimum viable instance count (3) reached. There are " + string(instance_count) + " instances running.")
		} else {
			t.Error("ERROR! Minimum viable instance count (3) not reached. There are " + string(instance_count) + " instances running.")
		}
	})

    // TEST 4: Veriy the number of Consul locks each instance is aware about. Should be exactly 1 lock on each instnace
	t.Run("Consul verifications", func(t *testing.T) {

	        test = assert.True(t, ConsulLockCheck(t, publicIPs, sshKey))
	        if test {
		        t.Log("INFO. Consul lock check passed. Each Consul node can see exactly 1 lock.")
		}

    // TEST 5: All of the Consul nodes should be healthy
		test = assert.True(t, ConsulCheck(t, publicIPs, sshKey))
	        if test {
		        t.Log("INFO. Consul check passed. Each node can see full cluster, all nodes are healthy")
		}


	})

	t.Run("Polkadot verifications", func(t *testing.T) {

    // TEST 6: Verify that there is only one Polkadot node working in Validator mode at a time
		test = assert.True(t, LeadersCheck(t, publicIPs, sshKey))
		if test {
			t.Log("INFO. Leaders check passed. Exactly 1 leader found")
		}
        
    // TEST 7: Verify that all Polkadot nodes are health
		test = assert.True(t, PolkadotCheck(t, publicIPs, sshKey))
		if test {
			t.Log("INFO. Polkadot node check passed. All instances are healthy")
		}

	})
    
    // TEST 8: All the validator keys were successfully uploaded to SSM in each region
	t.Run("SSM tests", func(t *testing.T) {

		test = assert.True(t, SSMCheck(t))
		if test {
			t.Log("INFO. All keys were uploaded. Private key is encrypted.")
		}
	})

    // TEST 9: Verify that all the groups that are used by the nodes are valid and contains verified rules only.
	t.Run("Security groups tests", func(t *testing.T) {

		test = assert.True(t, SGCheck(t))
		if test {
			t.Log("INFO. Security groups contains only an appropriate set of rules.")
		}
	})

    // TEST 10: Check that there are no unassigned volumes after the nodes started
	t.Run("Volumes tests", func(t *testing.T) {

                test = assert.True(t, VolumesCheck(t))
                if test {
                        t.Log("INFO. No disks left unattached.")
                } else {
			t.Error("WARNING! An unattached disk was detected with prefix " + prefix)
		}
        })
    
    // TEST 11: Check that no CloudWatch alarm were triggered
	t.Run("CloudWatch tests", func(t *testing.T) {

                test = assert.True(t, CloudWatchCheck(t))
                if test {
                        t.Log("INFO. All Cloud Watch alarms were created. No Cloud Watch alarm were triggered.")
                } else {
			t.Error("ERROR! Cloud Watch alarms are not in a good state")
		}
        })

    // TEST 12: Check that ELB and each target group confirms that all the instances are healthy
	t.Run("NLB tests", func(t *testing.T) {

                test = assert.True(t, NLBCheck(t,terraform.OutputList(t, terraformOptions, "lbs")))
                if test {
                        t.Log("INFO. NLB is configured. All target groups do exists. Health checks responds that instance state is OK.")
                }
        })
    // TEST 13: Check that there are exactly 5 keys in the keystore
        t.Run("Keystore tests", func(t *testing.T) {

                test = assert.True(t, KeystoreCheck(t,publicIPs, sshKey))
                if test {
                        t.Log("INFO. There are exactly 5 keys in the Keystore")
                }
        })

}

// TEST 9
func SGCheck(t *testing.T) bool {

    // A set of predefined security rules to compare existing rules with.
	fromPorts := []int64{30333, 22, 8301, 8600, 8500, 8300}
	toPorts := []int64{30333, 22, 8302, 8600, 8500}
	ipProtocols := []string{"tcp","udp"}
        cidrIPs := []string{"0.0.0.0/0","10.2.0.0/16", "10.1.0.0/16", "10.0.0.0/16"}
    
	var rules = []*ec2.IpPermission {
		&ec2.IpPermission {
		  FromPort: &fromPorts[0],
		  IpProtocol: &ipProtocols[0],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[0],
		    },
		  },
		  ToPort: &toPorts[0],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[1],
		  IpProtocol: &ipProtocols[0],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp:&cidrIPs[0],
		    },
		  },
		  ToPort: &toPorts[1],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[2],
		  IpProtocol: &ipProtocols[1],
		  IpRanges: []*ec2.IpRange {
		   &ec2.IpRange {
		      CidrIp: &cidrIPs[1],
		    },
		   &ec2.IpRange {
		      CidrIp: &cidrIPs[2],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[3],
		    },
		  },
		  ToPort:&toPorts[2],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[3],
		  IpProtocol: &ipProtocols[1],
		  IpRanges: []*ec2.IpRange {
		   &ec2.IpRange {
		      CidrIp: &cidrIPs[2],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[3],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[1],
		    },
		  },
		  ToPort: &toPorts[3],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[4],
		  IpProtocol: &ipProtocols[1],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[1],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[2],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[3],
		    },
		  },
		  ToPort: &toPorts[4],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[0],
		  IpProtocol: &ipProtocols[1],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[0],
		    },
		  },
		  ToPort: &toPorts[0],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[4],
		  IpProtocol: &ipProtocols[0],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[3],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[2],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[1],
		    },
		  },
		  ToPort: &toPorts[4],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[5],
		  IpProtocol: &ipProtocols[0],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[3],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[2],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[1],
		    },
		  },
		  ToPort: &toPorts[2],
		},
		&ec2.IpPermission {
		  FromPort: &fromPorts[3],
		  IpProtocol: &ipProtocols[0],
		  IpRanges: []*ec2.IpRange {
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[1],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[3],
		    },
		    &ec2.IpRange {
		      CidrIp: &cidrIPs[2],
		    },
		  },
		  ToPort: &toPorts[3],
		},
	}

    // For each region fetch all the security groups prefixed with predefined prefix and compare it one by one with a list of predefined groups
	for _, region := range awsRegion {

		ruleSlice := GetSGRulesMapByTag(t, region, "prefix", prefix)
		lenRuleSlice := len(ruleSlice)

		if lenRuleSlice != 9 {
			t.Error("ERROR! Expecting to get 9 security groups, got " + string(lenRuleSlice))
			return false
		}

		for _, ruleSet := range ruleSlice {
			found := 0
			for _, ruleExpect := range rules {
				if cmp.Equal(ruleSet,ruleExpect) {
					found = 1
					continue
				} else {
					t.Log(cmp.Diff(ruleSet, ruleExpect))
				}
			}
			if found != 1 {
				t.Error("ERROR! No match were found for current record:")
				t.Error(ruleSet)
				return false
			} else {
				t.Log("INFO. The following record matches one of the predefined security rules:")
				t.Log(ruleSet)
			}
		}
	}

	return true
}

// TEST 10
func VolumesCheck(t *testing.T) bool {

	count := 0
    // Go through each region. Select unattached labeled disks. If no disks found, then the test passes successfully
	for _, region := range awsRegion {

		check := GetVolumeDescribe(t, region, "prefix", prefix)

		if len(check) == 0 {
			t.Log("No unnatached disks were found in region " + region)
			continue
                } else {
			t.Error("Unattached disks were found in region " + region)
			count = count +1
		}

        }

        if( count == 0 ) {
		return true
	} else {
		return false
	}

}

// TEST 11
func CloudWatchCheck(t *testing.T) bool {

	count := 0
	for _, region := range awsRegion {
		for {
			insufficient_data_flag := false
			check := make(map[string]string)
			check = GetAlarmsNamesAndStatesByPrefix(t, region, prefix)
			lencheck := len(check)

            // Check that there are exactly 4 CloudWatch alarms (should be changed here if new alarms added)
			if lencheck != 4 {
				t.Error("ERROR! It is expected to have 4 CloudWatch Alarms in total, got " + string(lencheck))
				continue
			} else {
				t.Log("INFO. CloudWatch Alarms number matches the predefined value of 4")
			}

            // If alarm still has "INSUFFICIENT DATA" status - we need to wait until alarm either triggers or move into "OK" state.
			for k,v := range check {
				if v == "OK" {
					t.Log("INFO. The CloudWatch Alarm " + k + " in region " + region + " has the state OK!")
					continue
				} else if v == "INSUFFICIENT_DATA" {
					t.Log("INFO. The CloudWatch Alarm " + k + " in region " + region + " has insufficient data right now.")
					insufficient_data_flag = true
					break
				} else {
					t.Error("ERROR! The CloudWatch Alarm " + k + " in region " + region + " has the state " + v + ", which is not OK")
					count = count +1
				}
			}

            // If some of the alarms has insufficient data state - rerun all the checks once again.
			if !insufficient_data_flag {
				break;
			} else {
				t.Log("Sleeping 10 seconds before retrying...")
				time.Sleep(10 * time.Second)
			}
		}

        }

        if( count == 0 ) {
		return true
	} else {
		return false
	}

}

// TEST 12
func NLBCheck(t *testing.T, lbs []string) bool {
	var err bool = false
	for i, lb := range lbs {
		var errLocal bool = false

		resultMap := GetHealthStatusSliceByLBsARN(t, awsRegion[i], lb)
		lenResultMap := len(resultMap)

        // Check that there exactly 6 TargetGroup were created
		if lenResultMap != 6 {
			t.Error ("ERROR! Expected 6 TGs at LoadBalancer " + lb + ", got " + string(lenResultMap))
			err = true
		} else {
			t.Log ("INFO. There are exactly 6 TGs at LoadBalancer " + lb)
		}

        // Check that TG reports healthy status
		for TG, result := range resultMap {
			if result != "healthy" {
				t.Error("DEBUG. The LB " + lb + " contains TG " + TG + " with not healthy instances. Instance health status is " + result)
				err = true
				errLocal = true
			}
		}
		if errLocal {
			t.Log("ERROR! The LB " + lb + " contains some TG that are not healthy")
		} else {
			t.Log("All TGs in LB " + lb + " contains only healthy instances.")
		}
	}
	if err {
		return false
	} else {
		return true
	}
}

// Supplementary function: Checks that given parameter in each parameter exists and has the right type (e.g. all the encrypted parameters has the SecureString type)
func TypeAndValueComparator(t *testing.T, relativePath string, expectedType string, expectedValue string) int {

	for _, region := range awsRegion {
		ssmType, ssmValue := GetParameterTypeAndValue(t, region, "/polkadot/validator-failover/" + prefix + "/" + relativePath)
		if ssmType == expectedType && ssmValue == expectedValue {
			t.Log("INFO. SSM Parameter " + relativePath + " of type " + ssmType + " and value " + ssmValue + " at region " + region + " matched prefedined value.")
		} else {
			t.Error("ERROR! No match for SSM parameter " + relativePath + " at region " + region + ". Expected type: " + expectedType + ", expected value: " + expectedValue + ". Actual type: " + ssmType + ", actual value: " + ssmValue)
			return 0
		}
	}

	return 1
}

// TEST 8
func SSMCheck(t *testing.T) bool {

	result := TypeAndValueComparator(t, "cpu_limit",      "String",        "1") *
		  TypeAndValueComparator(t, "ram_limit",      "String",        "1") *
		  TypeAndValueComparator(t, "name",           "String",        "test") *
		  TypeAndValueComparator(t, "keys/key1/type", "String",        "gran") *
		  TypeAndValueComparator(t, "keys/key1/seed", "SecureString", "favorite liar zebra assume hurt cage any damp inherit rescue delay panic") *
		  TypeAndValueComparator(t, "keys/key1/key",  "String",        "0x6ce96ae5c300096b09dbd4567b0574f6a1281ae0e5cfe4f6b0233d1821f6206b") *
		  TypeAndValueComparator(t, "keys/key2/type", "String",        "aura") *
		  TypeAndValueComparator(t, "keys/key2/seed", "SecureString", "expire stage crawl shell boss any story swamp skull yellow bamboo copy") *
		  TypeAndValueComparator(t, "keys/key2/key",  "String",        "0x3ff0766f9ebbbceee6c2f40d9323164d07e70c70994c9d00a9512be6680c2394")

	if result == 1 {
		return true
	} else {
		return false
	}

}

// TEST 6
func LeadersCheck(t *testing.T, publicIPs map[string]string, key *ssh.KeyPair) bool {

  command := "curl -s -H \"Content-Type: application/json\" -d '{\"id\":1, \"jsonrpc\":\"2.0\", \"method\": \"system_nodeRoles\", \"params\":[]}' http://localhost:9933"
  array := NodeQuery(t, publicIPs, key, command)

  t.Log("Leaders output: " + strings.Join(array, ","))

  var leaders, nodes int = 0, 0

  if len(array) == 0 {
	  return false
  } else {

    // SSH into the node and ensure, that only one node returns "Authority" for system_nodeRoles call
    for _, value := range array {

      if value == "{\"jsonrpc\":\"2.0\",\"result\":[\"Authority\"],\"id\":1}" {
	      leaders++
      } else if value == "{\"jsonrpc\":\"2.0\",\"result\":[\"Full\"],\"id\":1}" {
	      nodes++
      } else {
	      t.Error("ERROR! Node working not in Full, not in Authority mode.")
	      return false
      }
    }
  }

  if leaders == 1 && nodes == len(publicIPs) - leaders {
	  t.Log("INFO. There are exactly one leader and the rest nodes are all working in a Full mode")
	  return true
  } else if leaders > 1 {
	  t.Error("ERROR! There are more than 1 leader at the same time.")
	  return false
  } else if leaders < 1 {
	  t.Error("ERROR! There are no leaders.")
	  return false
  } else {
	  t.Error("ERROR! Some of the full nodes are not working correctly.")
	  return false
  }
}

// TEST 7
func PolkadotCheck(t *testing.T, publicIPs map[string]string, key *ssh.KeyPair) bool {

  command := "curl -s -H \"Content-Type: application/json\" -d '{\"id\":1, \"jsonrpc\":\"2.0\", \"method\": \"system_health\", \"params\":[]}' http://localhost:9933"
  array := NodeQuery(t, publicIPs, key, command)

  t.Log("Leaders output: " + strings.Join(array, ","))

  if len(array) == 0 {
	  return false
  } else {

// Parse JSON and verify that node has healthy state
    for _,v := range array {

      type resultHealth struct {
	      peers int
	      shouldHavePeers bool
	      isSyncing bool
      }

      type Health struct {
	      jsonrpc string
	      result resultHealth
	      id int
      }

      var result Health
      err := json.Unmarshal([]byte(v), &result)

      if err != nil {
          t.Error("ERROR! " + err.Error())
	  return false
      }

      if result.result.peers < 2 && result.result.shouldHavePeers {
	  t.Error("ERROR! Node does not have enough peers")
	  return false
      }
    }
  }

  return true

}

// TEST 4
func ConsulLockCheck(t *testing.T, publicIPs map[string]string, key *ssh.KeyPair) bool {

  command := "consul kv export | grep \"prefix/.lock\" | wc -l"
  array := NodeQuery(t, publicIPs, key, command)

  if len(array) == 0 {
	  return false
  } else {

    for _, value := range array {

      intValue, err := strconv.Atoi(value)

      if err != nil {
	      t.Error("ERROR! " + err.Error())
	      return false
      }

      if intValue != 1 {
	      t.Error("ERROR! Error while retrieving Consul lock. Got: " + string(intValue) + " locks, should be exactly 1 lock.")
	      return false
      }
    }
  }

  return true

}

// TEST 13
func KeystoreCheck(t *testing.T, publicIPs map[string]string, key *ssh.KeyPair) bool {

  command := "ls -lah /data/chains/westend2/keystore | wc -l"

  for i:=0; i<5; i++ {
  	array := NodeQuery(t, publicIPs, key, command)

	  iterator := 0
	  flag := false

	  if len(array) == 0 {
		  return false
	  } else {

	    for _, value := range array {

	      var keysExpected string = "5"

	      if value != keysExpected {
		      t.Log("INFO. Lines at output: " + value + " (should be 3 lines more than keys expected)")
		      if value != "3" {
			      flag = true
		      }
		      iterator++
	      }
	    }
	  }

	  if flag {
		  t.Log("Seems that init script is still running, waiting...")
		  time.Sleep(30 * time.Second)
		  continue;
	  }

	  if iterator != 2 {
	    t.Error("ERROR! Keys count not matched. There should be exactly 5 keys on exactly 1 node.")
	    return false
	  }
	  return true
  }
  t.Log("Some keystore are either empty or not full")
  return false
}

// TEST 5
func ConsulCheck(t *testing.T, publicIPs map[string]string, key *ssh.KeyPair) bool {

  command := "consul members --status alive | wc -l"
  array := NodeQuery(t, publicIPs, key, command)

  if len(array) == 0 {
	  return false
  } else {

    for _, value := range array {

      intValue, err := strconv.Atoi(value)

      if err != nil {
	      t.Error("ERROR! " + err.Error())
	      return false
      }

      var instanceCountExpected int = len(publicIPs) + 1

      if intValue != instanceCountExpected {
	      t.Error("ERROR! Consul node count not matched. One of the nodes responded the following healthy instance count: " + string(intValue) + ", while there should be " + string(instanceCountExpected) + " instances")
	      return false
      }
    }
  }

  return true

}

// Supplementary function: perform given SSH query on the node 
func NodeQuery(t *testing.T, publicIPs map[string]string, key *ssh.KeyPair, command string) []string {

        var resultArray []string

        for _, publicInstanceIP := range publicIPs {

		publicHost := ssh.Host{
			Hostname:    publicInstanceIP,
			SshKeyPair:  key,
			SshUserName: "ec2-user",
		}

		// It can take a minute or so for the Instance to boot up, so retry a few times
		maxRetries := 10
		timeBetweenRetries := 5 * time.Second
		description := fmt.Sprintf("SSH to public host %s", publicInstanceIP)

		t.Log("DEBUG. Querying instance " + publicInstanceIP + " with command `" + command + "`")
		// Verify that we can SSH to the Instance and run commands
		result := retry.DoWithRetry(t, description, maxRetries, timeBetweenRetries, func() (string, error) {
			result, err := ssh.CheckSshCommandE(t, publicHost, command)

			if err != nil {
				return "", err
			}

			return strings.TrimSpace(result), nil
		})

		t.Log("DEBUG. Command output: " + result)
		resultArray = append(resultArray, result)
	}
	return resultArray
}
