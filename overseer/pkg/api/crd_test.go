package api

import (
	"context"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"sigs.k8s.io/yaml"
)

// TestCRDIsValid runs the apiserver's own CRD validation, including the
// CEL rule cost budget, over the generated CRD: a rule over a string with
// no maxLength passes controller-gen but fails kubectl apply.
func TestCRDIsValid(t *testing.T) {
	b, err := os.ReadFile("../../k8s/crds/overseer.gemini.google.com_overseers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var v1 apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(b, &v1); err != nil {
		t.Fatal(err)
	}
	apiextensionsv1.SetObjectDefaults_CustomResourceDefinition(&v1)
	var crd apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&v1, &crd, nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range validation.ValidateCustomResourceDefinition(context.Background(), &crd) {
		t.Error(e)
	}
}
