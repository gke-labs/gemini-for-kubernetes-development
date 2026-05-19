package sandbox

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// DevSandboxOptions holds common options for creating Sandboxes.
type DevSandboxOptions struct {
	Name        string
	Namespace   string
	Labels      map[string]string
	Annotations map[string]string
	Image       string
	Replicas    int64
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
	if _, ok := resources.Requests["ephemeral-storage"]; !ok {
		resources.Requests["ephemeral-storage"] = resource.MustParse("6Gi")
	}
	if _, ok := resources.Limits["ephemeral-storage"]; !ok {
		resources.Limits["ephemeral-storage"] = resource.MustParse("6Gi")
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

	env := []interface{}{}

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
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":    "sandbox",
								"image":   opt.Image,
								"command": []interface{}{"envd"},
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
								"env": env,
								"volumeMounts": []interface{}{
									map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
								},
								"ports": []interface{}{
									map[string]interface{}{"containerPort": int64(13337)},
									map[string]interface{}{"containerPort": int64(49983)},
								},
							},
						},
					},
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
									"storage": "10Gi",
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

	env := []interface{}{}

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
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":    "sandbox",
								"image":   opt.Image,
								"command": []interface{}{"envd"},
								"resources": map[string]interface{}{
									"limits": map[string]interface{}{
										"ephemeral-storage": "6Gi",
									},
									"requests": map[string]interface{}{
										"ephemeral-storage": "6Gi",
									},
								},
								"env": env,
								"volumeMounts": []interface{}{
									map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
								},
								"ports": []interface{}{
									map[string]interface{}{"containerPort": int64(13337)},
									map[string]interface{}{"containerPort": int64(49983)},
								},
							},
						},
					},
				},
				"volumeClaimTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{"name": "workspaces-pvc"},
						"spec": map[string]interface{}{
							"accessModes": []interface{}{"ReadWriteOnce"},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"storage": "10Gi",
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
