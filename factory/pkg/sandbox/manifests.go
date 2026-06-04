package sandbox

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	GoCachePath    = "/workspaces/.cache/go-build"
	GoModCachePath = "/workspaces/.cache/mod"
	TmpDirPath     = "/workspaces/.tmp"
)

type SecretMount struct {
	Name      string
	MountPath string
}

type EnvVar struct {
	Name  string
	Value string
}

// DevSandboxOptions holds common options for creating Sandboxes.
type DevSandboxOptions struct {
	Name              string
	Namespace         string
	Labels            map[string]string
	Annotations       map[string]string
	Image             string
	Replicas          int64
	WorkspaceDiskSize string
	EphemeralStorage  string
	Secrets           []SecretMount
	Env               []EnvVar
}

// AgentSandboxOptions holds options for creating an AgentSandbox.
type AgentSandboxOptions struct {
	DevSandboxOptions

	Resources corev1.ResourceRequirements
}

// ReviewSandboxOptions holds options for creating a ReviewSandbox.
type ReviewSandboxOptions struct {
	DevSandboxOptions

	PRNumber   int
	PRTitle    string
	PRHTMLURL  string
	PRDiffURL  string
	PRCloneURL string
	RepoName   string
}

func stringPtr(s string) *string {
	return &s
}

// NewAgentSandbox creates a new Sandbox (unstructured) and Service object.
func NewAgentSandbox(opt AgentSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	sandboxName := opt.Name
	resources := opt.Resources
	if resources.Requests == nil {
		resources.Requests = make(corev1.ResourceList)
	}
	if resources.Limits == nil {
		resources.Limits = make(corev1.ResourceList)
	}
	if resources.Requests.Memory().IsZero() {
		resources.Requests[corev1.ResourceMemory] = resource.MustParse("2Gi")
	}
	if resources.Limits.Memory().IsZero() {
		resources.Limits[corev1.ResourceMemory] = resource.MustParse("6Gi")
	}
	if resources.Requests.Cpu().IsZero() {
		resources.Requests[corev1.ResourceCPU] = resource.MustParse("2000m")
	}
	if resources.Limits.Cpu().IsZero() {
		resources.Limits[corev1.ResourceCPU] = resource.MustParse("4000m")
	}

	ephemeralStorage := opt.EphemeralStorage
	if ephemeralStorage == "" {
		ephemeralStorage = "6Gi"
	}
	if _, ok := resources.Requests["ephemeral-storage"]; !ok {
		resources.Requests["ephemeral-storage"] = resource.MustParse(ephemeralStorage)
	}
	if _, ok := resources.Limits["ephemeral-storage"]; !ok {
		resources.Limits["ephemeral-storage"] = resource.MustParse(ephemeralStorage)
	}

	labelsInterface := make(map[string]interface{}, len(opt.Labels)+1)
	for k, v := range opt.Labels {
		labelsInterface[k] = v
	}
	labelsInterface["sandbox"] = sandboxName

	annotationsInterface := make(map[string]interface{}, len(opt.Annotations))
	for k, v := range opt.Annotations {
		annotationsInterface[k] = v
	}

	ephemeralRequest := resources.Requests["ephemeral-storage"]
	ephemeralLimit := resources.Limits["ephemeral-storage"]

	diskSize := opt.WorkspaceDiskSize
	if diskSize == "" {
		diskSize = "10Gi"
	}

	env := []interface{}{
		map[string]interface{}{"name": "GOCACHE", "value": GoCachePath},
		map[string]interface{}{"name": "GOMODCACHE", "value": GoModCachePath},
		map[string]interface{}{"name": "TMPDIR", "value": TmpDirPath},
		map[string]interface{}{"name": "GOTMPDIR", "value": TmpDirPath},
	}
	for _, e := range opt.Env {
		env = append(env, map[string]interface{}{
			"name":  e.Name,
			"value": e.Value,
		})
	}

	volumeMounts := []interface{}{
		map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
	}
	for _, secret := range opt.Secrets {
		volumeMounts = append(volumeMounts, map[string]interface{}{
			"name":      "secret-vol-" + secret.Name,
			"mountPath": secret.MountPath,
		})
	}

	var volumesList []interface{}
	for _, secret := range opt.Secrets {
		volumesList = append(volumesList, map[string]interface{}{
			"name": "secret-vol-" + secret.Name,
			"secret": map[string]interface{}{
				"secretName": secret.Name,
			},
		})
	}

	podSpecMap := map[string]interface{}{
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "sandbox",
				"image":   opt.Image,
				"command": []interface{}{"factory", "daemon"},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":               resources.Requests.Cpu().String(),
						"memory":            resources.Requests.Memory().String(),
						"ephemeral-storage": ephemeralRequest.String(),
					},
					"limits": map[string]interface{}{
						"cpu":               resources.Limits.Cpu().String(),
						"memory":            resources.Limits.Memory().String(),
						"ephemeral-storage": ephemeralLimit.String(),
					},
				},
				"env":          env,
				"volumeMounts": volumeMounts,
				"ports": []interface{}{
					map[string]interface{}{"containerPort": int64(13337)},
					map[string]interface{}{"containerPort": int64(49983)},
				},
			},
		},
	}
	if len(volumesList) > 0 {
		podSpecMap["volumes"] = volumesList
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":        sandboxName,
				"namespace":   opt.Namespace,
				"labels":      labelsInterface,
				"annotations": annotationsInterface,
			},
			"spec": map[string]interface{}{
				"replicas": opt.Replicas,
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": labelsInterface,
					},
					"spec": podSpecMap,
				},
				"volumeClaimTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "workspaces-pvc",
						},
						"spec": map[string]interface{}{
							"accessModes": []interface{}{"ReadWriteOnce"},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"storage": diskSize,
								},
							},
						},
					},
				},
			},
		},
	}

	serviceName := sandboxName + "-lb"
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: opt.Namespace,
			Labels:    opt.Labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"sandbox": sandboxName,
			},
			Ports: []corev1.ServicePort{
				{
					Name:        "code-server",
					Protocol:    corev1.ProtocolTCP,
					Port:        13338,
					TargetPort:  intstr.FromInt(13337),
					AppProtocol: stringPtr("kubernetes.io/ws"),
				},
				{
					Name:       "envd",
					Protocol:   corev1.ProtocolTCP,
					Port:       49983,
					TargetPort: intstr.FromInt(49983),
				},
			},
		},
	}

	return sandbox, service
}

// NewReviewSandbox creates a new ReviewSandbox and Service object.
func NewReviewSandbox(opt ReviewSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	sandboxName := opt.Name
	labels := make(map[string]interface{})
	for k, v := range opt.Labels {
		labels[k] = v
	}
	labels["sandbox"] = sandboxName
	labels["sandbox-type"] = "review"
	labels["factory.gemini.google.com/pr"] = fmt.Sprintf("%d", opt.PRNumber)

	annotations := make(map[string]interface{})
	for k, v := range opt.Annotations {
		annotations[k] = v
	}
	annotations["pr"] = fmt.Sprintf("%d", opt.PRNumber)
	annotations["title"] = opt.PRTitle
	annotations["repo"] = opt.RepoName
	annotations["htmlURL"] = opt.PRHTMLURL
	annotations["diffURL"] = opt.PRDiffURL
	annotations["cloneURL"] = opt.PRCloneURL

	diskSize := opt.WorkspaceDiskSize
	if diskSize == "" {
		diskSize = "10Gi"
	}

	ephemeralStorage := opt.EphemeralStorage
	if ephemeralStorage == "" {
		ephemeralStorage = "6Gi"
	}

	env := []interface{}{
		map[string]interface{}{"name": "GOCACHE", "value": GoCachePath},
		map[string]interface{}{"name": "GOMODCACHE", "value": GoModCachePath},
		map[string]interface{}{"name": "TMPDIR", "value": TmpDirPath},
		map[string]interface{}{"name": "GOTMPDIR", "value": TmpDirPath},
	}
	for _, e := range opt.Env {
		env = append(env, map[string]interface{}{
			"name":  e.Name,
			"value": e.Value,
		})
	}

	volumeMounts := []interface{}{
		map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
	}
	for _, secret := range opt.Secrets {
		volumeMounts = append(volumeMounts, map[string]interface{}{
			"name":      "secret-vol-" + secret.Name,
			"mountPath": secret.MountPath,
		})
	}

	var volumesList []interface{}
	for _, secret := range opt.Secrets {
		volumesList = append(volumesList, map[string]interface{}{
			"name": "secret-vol-" + secret.Name,
			"secret": map[string]interface{}{
				"secretName": secret.Name,
			},
		})
	}

	podSpecMap := map[string]interface{}{
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "sandbox",
				"image":   opt.Image,
				"command": []interface{}{"factory", "daemon"},
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{
						"ephemeral-storage": ephemeralStorage,
					},
					"requests": map[string]interface{}{
						"ephemeral-storage": ephemeralStorage,
					},
				},
				"env":          env,
				"volumeMounts": volumeMounts,
				"ports": []interface{}{
					map[string]interface{}{"containerPort": int64(13337)},
					map[string]interface{}{"containerPort": int64(49983)},
				},
			},
		},
	}
	if len(volumesList) > 0 {
		podSpecMap["volumes"] = volumesList
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":        sandboxName,
				"namespace":   opt.Namespace,
				"labels":      labels,
				"annotations": annotations,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"sandbox": sandboxName,
						},
					},
					"spec": podSpecMap,
				},
				"volumeClaimTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{"name": "workspaces-pvc"},
						"spec": map[string]interface{}{
							"accessModes": []interface{}{"ReadWriteOnce"},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"storage": diskSize,
								},
							},
						},
					},
				},
			},
		},
	}

	serviceName := fmt.Sprintf("%s-lb", sandboxName)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: opt.Namespace,
			Labels:    opt.Labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"sandbox": sandboxName,
			},
			Ports: []corev1.ServicePort{
				{
					Name:        "code-server",
					Protocol:    corev1.ProtocolTCP,
					Port:        13338,
					TargetPort:  intstr.FromInt(13337),
					AppProtocol: stringPtr("kubernetes.io/ws"),
				},
				{
					Name:       "envd",
					Protocol:   corev1.ProtocolTCP,
					Port:       49983,
					TargetPort: intstr.FromInt(49983),
				},
			},
		},
	}

	return sandbox, service
}
