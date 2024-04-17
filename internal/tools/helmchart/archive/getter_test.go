//go:build integration
// +build integration

package archive_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/helmchart/archive"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	cacheddiscovery "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

func TestGetter(t *testing.T) {
	rc, err := newRestConfig()
	if err != nil {
		t.Fatal(err)
	}

	uns, err := getUnstructured(rc, getUnstructuredOptions{
		gvk:       schema.FromAPIVersionAndKind("composition.krateo.io/v0-1-0", "FireworksApp"),
		name:      "test-1",
		namespace: "demo-system",
	})
	if err != nil {
		t.Fatal(err)
	}

	gt, err := archive.Dynamic(rc, true)
	if err != nil {
		t.Fatal(err)
	}

	nfo, err := gt.Get(uns)
	if err != nil {
		t.Fatal(err)
	}

	spew.Dump(nfo)

}

type getUnstructuredOptions struct {
	gvk       schema.GroupVersionKind
	name      string
	namespace string
}

func getUnstructured(rc *rest.Config, opts getUnstructuredOptions) (*unstructured.Unstructured, error) {
	dynamicClient, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(
		cacheddiscovery.NewMemCacheClient(discoveryClient),
	)

	restMapping, err := mapper.RESTMapping(opts.gvk.GroupKind(), opts.gvk.Version)
	if err != nil {
		return nil, err
	}

	var ri dynamic.ResourceInterface
	if restMapping.Scope.Name() == meta.RESTScopeNameRoot {
		ri = dynamicClient.Resource(restMapping.Resource)
	} else {
		ri = dynamicClient.Resource(restMapping.Resource).
			Namespace(opts.namespace)
	}

	return ri.Get(context.TODO(), opts.name, metav1.GetOptions{})
}

func newRestConfig() (*rest.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
}
