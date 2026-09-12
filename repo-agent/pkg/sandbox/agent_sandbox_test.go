package sandbox

import (
	"testing"
)

func TestNewAgentSandbox(t *testing.T) {
	tests := []struct {
		name              string
		dindSupport       string
		workspaceDiskSize string
	}{
		{
			name:        "DindSupportNone",
			dindSupport: DindSupportNone,
		},
		{
			name:        "DindSupportEmpty",
			dindSupport: "",
		},
		{
			name:        "DindSupportGvisor",
			dindSupport: DindSupportGvisor,
		},
		{
			name:        "DindSupportPrivileged",
			dindSupport: DindSupportPrivileged,
		},
		{
			name:              "WorkspaceDiskSizeCustom",
			workspaceDiskSize: "30Gi",
		},
		{
			name:              "WorkspaceDiskSizeDefault",
			workspaceDiskSize: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := AgentSandboxOptions{
				DevSandboxOptions: DevSandboxOptions{
					Name:              "test",
					Namespace:         "default",
					WorkspaceDiskSize: tt.workspaceDiskSize,
				},
				DindSupport: tt.dindSupport,
			}
			sandbox, _ := NewAgentSandbox(opt)

			spec := sandbox.Object["spec"].(map[string]interface{})

			// Check WorkspaceDiskSize
			volumeClaimTemplates := spec["volumeClaimTemplates"].([]interface{})
			foundWorkspacesPVC := false
			for _, vct := range volumeClaimTemplates {
				vctMap := vct.(map[string]interface{})
				metadata := vctMap["metadata"].(map[string]interface{})
				if metadata["name"] == "workspaces-pvc" {
					foundWorkspacesPVC = true
					vctSpec := vctMap["spec"].(map[string]interface{})
					resources := vctSpec["resources"].(map[string]interface{})
					requests := resources["requests"].(map[string]interface{})
					storage := requests["storage"].(string)

					expectedStorage := "10Gi"
					if tt.workspaceDiskSize != "" {
						expectedStorage = tt.workspaceDiskSize
					}
					if storage != expectedStorage {
						t.Errorf("expected storage %s, got %s", expectedStorage, storage)
					}
					break
				}
			}
			if !foundWorkspacesPVC {
				t.Errorf("workspaces-pvc volumeClaimTemplate not found")
			}

			podTemplate := spec["podTemplate"].(map[string]interface{})
			podSpec := podTemplate["spec"].(map[string]interface{})

			// Check runtimeClassName
			runtimeClassName := podSpec["runtimeClassName"]
			if tt.dindSupport == DindSupportGvisor {
				if runtimeClassName != "gvisor" {
					t.Errorf("expected runtimeClassName gvisor, got %v", runtimeClassName)
				}
			} else {
				if runtimeClassName != nil {
					t.Errorf("expected runtimeClassName nil, got %v", runtimeClassName)
				}
			}

			// Check dnsPolicy and dnsConfig
			dnsPolicy := podSpec["dnsPolicy"]
			dnsConfig := podSpec["dnsConfig"]
			isDind := tt.dindSupport == DindSupportGvisor || tt.dindSupport == DindSupportPrivileged
			if isDind {
				if dnsPolicy != "None" {
					t.Errorf("expected dnsPolicy None, got %v", dnsPolicy)
				}
				if dnsConfig == nil {
					t.Errorf("expected dnsConfig not nil")
				}
			} else {
				if dnsPolicy != nil {
					t.Errorf("expected dnsPolicy nil, got %v", dnsPolicy)
				}
				if dnsConfig != nil {
					t.Errorf("expected dnsConfig nil, got %v", dnsConfig)
				}
			}

			// Check container securityContext
			containers := podSpec["containers"].([]interface{})
			container := containers[0].(map[string]interface{})
			securityContext := container["securityContext"].(map[string]interface{})

			if tt.dindSupport == DindSupportPrivileged {
				if securityContext["privileged"] != true {
					t.Errorf("expected privileged true, got %v", securityContext["privileged"])
				}
			} else if tt.dindSupport == DindSupportGvisor {
				if securityContext["privileged"] != nil {
					t.Errorf("expected privileged nil, got %v", securityContext["privileged"])
				}
				capabilities := securityContext["capabilities"].(map[string]interface{})
				add := capabilities["add"].([]interface{})
				if len(add) == 0 {
					t.Errorf("expected capabilities.add to be non-empty")
				}
			} else {
				if securityContext["privileged"] != nil {
					t.Errorf("expected privileged nil, got %v", securityContext["privileged"])
				}
				if securityContext["capabilities"] != nil {
					t.Errorf("expected capabilities nil, got %v", securityContext["capabilities"])
				}
			}

			// Check volumes
			volumes := podSpec["volumes"].([]interface{})
			hasDockerVolume := false
			for _, v := range volumes {
				vol := v.(map[string]interface{})
				if vol["name"] == "docker" {
					hasDockerVolume = true
					break
				}
			}
			if isDind != hasDockerVolume {
				t.Errorf("expected hasDockerVolume %v, got %v", isDind, hasDockerVolume)
			}

			// Check volumeMounts
			volumeMounts := container["volumeMounts"].([]interface{})
			hasDockerVolumeMount := false
			for _, vm := range volumeMounts {
				vmount := vm.(map[string]interface{})
				if vmount["name"] == "docker" {
					hasDockerVolumeMount = true
					break
				}
			}
			if isDind != hasDockerVolumeMount {
				t.Errorf("expected hasDockerVolumeMount %v, got %v", isDind, hasDockerVolumeMount)
			}

			// Check DIND_SUPPORT env var
			env := container["env"].([]interface{})
			foundDindEnv := false
			for _, e := range env {
				envVar := e.(map[string]interface{})
				if envVar["name"] == "DIND_SUPPORT" {
					foundDindEnv = true
					if envVar["value"] != tt.dindSupport {
						t.Errorf("expected DIND_SUPPORT env %v, got %v", tt.dindSupport, envVar["value"])
					}
					break
				}
			}
			if !foundDindEnv {
				t.Errorf("DIND_SUPPORT env var not found")
			}

			// Check Go cache and tmp env vars
			expectedEnv := map[string]string{
				"GOCACHE":                    GoCachePath,
				"GOMODCACHE":                 GoModCachePath,
				"TMPDIR":                     TmpDirPath,
				"GOTMPDIR":                   TmpDirPath,
				"GEMINI_CLI_TRUST_WORKSPACE": "true",
				"GOTOOLCHAIN":                "local",
			}

			for name, value := range expectedEnv {
				found := false
				for _, e := range env {
					envVar := e.(map[string]interface{})
					if envVar["name"] == name {
						found = true
						if envVar["value"] != value {
							t.Errorf("expected %s env %s, got %v", name, value, envVar["value"])
						}
						break
					}
				}
				if !found {
					t.Errorf("%s env var not found", name)
				}
			}
		})
	}
}

func TestSandboxGPU(t *testing.T) {
	// Test NewAgentSandbox with GPU enabled
	optAgent := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      "test-gpu-agent",
			Namespace: "default",
			GPU:       true,
		},
	}
	sandboxAgent, _ := NewAgentSandbox(optAgent)

	specAgent := sandboxAgent.Object["spec"].(map[string]interface{})
	podTemplateAgent := specAgent["podTemplate"].(map[string]interface{})
	podSpecAgent := podTemplateAgent["spec"].(map[string]interface{})

	// Check nodeSelector
	nodeSelectorAgent, ok := podSpecAgent["nodeSelector"].(map[string]interface{})
	if !ok || nodeSelectorAgent["cloud.google.com/gke-gpu-sharing-strategy"] != "time-sharing" {
		t.Errorf("expected nodeSelector for GKE GPU sharing, got %v", podSpecAgent["nodeSelector"])
	}

	// Check container resource limits
	containersAgent := podSpecAgent["containers"].([]interface{})
	containerAgent := containersAgent[0].(map[string]interface{})
	resourcesAgent := containerAgent["resources"].(map[string]interface{})
	limitsAgent := resourcesAgent["limits"].(map[string]interface{})
	if limitsAgent["nvidia.com/gpu"] != "1" {
		t.Errorf("expected nvidia.com/gpu limit to be 1, got %v", limitsAgent["nvidia.com/gpu"])
	}

	// Test NewReviewSandbox with GPU enabled
	optReview := ReviewSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      "test-gpu-review",
			Namespace: "default",
			GPU:       true,
		},
	}
	sandboxReview, _ := NewReviewSandbox(optReview)

	specReview := sandboxReview.Object["spec"].(map[string]interface{})
	podTemplateReview := specReview["podTemplate"].(map[string]interface{})
	podSpecReview := podTemplateReview["spec"].(map[string]interface{})

	// Check nodeSelector
	nodeSelectorReview, ok := podSpecReview["nodeSelector"].(map[string]interface{})
	if !ok || nodeSelectorReview["cloud.google.com/gke-gpu-sharing-strategy"] != "time-sharing" {
		t.Errorf("expected nodeSelector for GKE GPU sharing, got %v", podSpecReview["nodeSelector"])
	}

	// Check container resource limits
	containersReview := podSpecReview["containers"].([]interface{})
	containerReview := containersReview[0].(map[string]interface{})
	resourcesReview := containerReview["resources"].(map[string]interface{})
	limitsReview := resourcesReview["limits"].(map[string]interface{})
	if limitsReview["nvidia.com/gpu"] != "1" {
		t.Errorf("expected nvidia.com/gpu limit to be 1, got %v", limitsReview["nvidia.com/gpu"])
	}
}
