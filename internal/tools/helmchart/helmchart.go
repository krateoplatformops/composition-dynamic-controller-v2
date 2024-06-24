package helmchart

import (
	"bytes"
	"context"
	"strings"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/controller"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/helmclient"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools"
	unstructuredtools "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	sigsyaml "sigs.k8s.io/yaml"

	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

func ExtractValuesFromSpec(un *unstructured.Unstructured) ([]byte, error) {
	if un == nil {
		return nil, nil
	}

	spec, ok, err := unstructured.NestedMap(un.UnstructuredContent(), "spec")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	return sigsyaml.Marshal(spec)
}

type RenderTemplateOptions struct {
	HelmClient     helmclient.Client
	PackageUrl     string
	PackageVersion string
	Resource       *unstructured.Unstructured
	Repo           string
	Credentials    *Credentials
}

func RenderTemplate(ctx context.Context, opts RenderTemplateOptions) ([]controller.ObjectRef, error) {
	dat, err := ExtractValuesFromSpec(opts.Resource)
	if err != nil {
		return nil, err
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName: opts.Resource.GetName(),
		Namespace:   opts.Resource.GetNamespace(),
		ChartName:   opts.PackageUrl,
		Version:     opts.PackageVersion,
		ValuesYaml:  string(dat),
		Repo:        opts.Repo,
	}
	if opts.Credentials != nil {
		chartSpec.Username = opts.Credentials.Username
		chartSpec.Password = opts.Credentials.Password
	}

	tpl, err := opts.HelmClient.TemplateChart(&chartSpec, nil)
	if err != nil {
		return nil, err
	}

	all := []controller.ObjectRef{}

	for _, spec := range strings.Split(string(tpl), "---") {
		if len(spec) == 0 {
			continue
		}

		decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(spec)), 100)

		var rawObj runtime.RawExtension
		if err = decoder.Decode(&rawObj); err != nil {
			return all, err
		}

		obj, gvk, err := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme).Decode(rawObj.Raw, nil, nil)
		if err != nil {
			return all, err
		}

		unstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return all, err
		}

		unstructuredObj := &unstructured.Unstructured{Object: unstructuredMap}
		if unstructuredObj.GetNamespace() == "" {
			unstructuredObj.SetNamespace(opts.Resource.GetNamespace())
		}

		_, ok, err := unstructured.NestedString(unstructuredMap, "metadata", "annotations", "helm.sh/hook")
		if ok || err != nil {
			continue
		}

		apiVersion, kind := gvk.ToAPIVersionAndKind()
		all = append(all, controller.ObjectRef{
			APIVersion: apiVersion,
			Kind:       kind,
			Name:       unstructuredObj.GetName(),
			Namespace:  unstructuredObj.GetNamespace(),
		})
	}

	return all, nil
}

type CheckResourceOptions struct {
	DynamicClient   dynamic.Interface
	DiscoveryClient *discovery.DiscoveryClient
}

func CheckResource(ctx context.Context, ref controller.ObjectRef, opts CheckResourceOptions) (*controller.ObjectRef, error) {
	gvr, err := tools.GVKtoGVR(opts.DiscoveryClient, schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind))
	if err != nil {
		return nil, err
	}

	un, err := opts.DynamicClient.Resource(gvr).
		Namespace(ref.Namespace).
		Get(ctx, ref.Name, metav1.GetOptions{})
	if un == nil {
		// Try to get the resource without the namespace. This is useful for cluster-scoped resources.
		un, err = opts.DynamicClient.Resource(gvr).
			Get(ctx, ref.Name, metav1.GetOptions{})
	}

	if err != nil {
		return nil, err
	}

	_, err = unstructuredtools.IsAvailable(un)
	if err != nil {
		if ex, ok := err.(*unstructuredtools.NotAvailableError); ok {
			return ex.FailedObjectRef, ex.Err
		}
	}

	return nil, err
}

func FindRelease(hc helmclient.Client, name string) (*release.Release, error) {
	all, err := hc.ListDeployedReleases()
	if err != nil {
		return nil, err
	}

	var res *release.Release
	for _, el := range all {
		if name == el.Name {
			res = el
			break
		}
	}

	return res, nil
}
