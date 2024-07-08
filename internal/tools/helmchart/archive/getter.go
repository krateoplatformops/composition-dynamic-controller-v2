package archive

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/client/helmclient"
	unstructuredtools "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type Info struct {
	// URL of the helm chart package that is being requested.
	URL string `json:"url"`

	// Version of the chart release.
	Version string `json:"version,omitempty"`

	// Repo is the repository name.
	Repo string `json:"repo,omitempty"`

	// RegistryAuth is the credentials to access the registry.
	RegistryAuth *helmclient.RegistryAuth `json:"registryAuth,omitempty"`
}

func (i *Info) IsOCI() bool {
	return strings.HasPrefix(i.URL, "oci://")
}

func (i *Info) IsTGZ() bool {
	return strings.HasSuffix(i.URL, ".tgz")
}

func (i *Info) IsHTTP() bool {
	return strings.HasPrefix(i.URL, "http://") || strings.HasPrefix(i.URL, "https://")
}

type Getter interface {
	Get(un *unstructured.Unstructured) (*Info, error)
}

func Static(chart string) Getter {
	return staticGetter{chartName: chart}
}

func Dynamic(cfg *rest.Config, verbose bool) (Getter, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	if verbose {
		log.SetOutput(os.Stderr)
	} else {
		log.SetOutput(io.Discard)
	}

	return &dynamicGetter{
		dynamicClient: dyn,
	}, nil
}

var _ Getter = (*staticGetter)(nil)

type staticGetter struct {
	chartName string
}

func (pig staticGetter) Get(_ *unstructured.Unstructured) (*Info, error) {
	return &Info{
		URL: pig.chartName,
	}, nil
}

var _ Getter = (*dynamicGetter)(nil)

type dynamicGetter struct {
	dynamicClient dynamic.Interface
}

func (g *dynamicGetter) Get(uns *unstructured.Unstructured) (*Info, error) {
	gvr, err := unstructuredtools.GVR(uns)
	if err != nil {
		return nil, err
	}
	log.Printf("[DBG] Infered GVR %s (kind: %s)\n", gvr.String(), uns.GetKind())

	gvrForDefinitions := schema.GroupVersionResource{
		Group:    "core.krateo.io",
		Version:  "v1alpha1",
		Resource: "compositiondefinitions",
	}

	all, err := g.dynamicClient.Resource(gvrForDefinitions).
		Namespace(uns.GetNamespace()).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	log.Printf("[DBG] Found %d resources of type: %s\n", len(all.Items), gvrForDefinitions)

	got := []*unstructured.Unstructured{}
	for _, el := range all.Items {
		apiVersion, ok, err := unstructured.NestedString(el.UnstructuredContent(), "status", "apiVersion")
		if err != nil {
			log.Printf("[ERR] resolving 'status.apiVersion': %s (%s@%s)\n", err.Error(), el.GetName(), el.GetNamespace())
			continue
		}
		if !ok {
			continue
		}

		kind, ok, err := unstructured.NestedString(el.UnstructuredContent(), "status", "kind")
		if err != nil {
			log.Printf("[ERR] resolving 'status.kind': %s (%s@%s)\n", err.Error(), el.GetName(), el.GetNamespace())
			continue
		}
		if !ok {
			continue
		}

		if apiVersion == uns.GetAPIVersion() && kind == uns.GetKind() {
			got = append(got, el.DeepCopy())
		}
	}

	tot := len(got)
	if tot == 0 {
		return nil,
			fmt.Errorf("no definition found for '%v' in namespace: %s", gvr, uns.GetNamespace())
	}

	if tot > 1 {
		return nil,
			fmt.Errorf("too many definitions [%d] found for '%v' in namespace: %s", tot, gvr, uns.GetNamespace())
	}

	packageUrl, ok, err := unstructured.NestedString(got[0].UnstructuredContent(), "spec", "chart", "url")
	if err != nil {
		log.Printf("[ERR] resolving 'status.packageUrl': %s (%s@%s)\n", err.Error(), got[0].GetName(), got[0].GetNamespace())
		return nil, err
	}
	if !ok {
		return nil,
			fmt.Errorf("missing 'status.packageUrl' in definition for '%v' in namespace: %s", gvr, uns.GetNamespace())
	}
	log.Printf("[DBG] packageUrl for (%s@%s): %s\n", got[0].GetName(), got[0].GetNamespace(), packageUrl)

	packageVersion, _, err := unstructured.NestedString(got[0].UnstructuredContent(), "spec", "chart", "version")
	if err != nil {
		log.Printf("[ERR] resolving 'spec.chart.version': %s (%s@%s)\n", err.Error(), got[0].GetName(), got[0].GetNamespace())
		return nil, err
	}
	repo, _, err := unstructured.NestedString(got[0].UnstructuredContent(), "spec", "chart", "repo")
	if err != nil {
		log.Printf("[ERR] resolving 'spec.chart.repo': %s (%s@%s)\n", err.Error(), got[0].GetName(), got[0].GetNamespace())
		return nil, err
	}

	username, _, err := unstructured.NestedString(got[0].UnstructuredContent(), "spec", "chart", "credentials", "username")
	if err != nil {
		log.Printf("[ERR] resolving 'spec.chart.credentials.username': %s (%s@%s)\n", err.Error(), got[0].GetName(), got[0].GetNamespace())
		return nil, err
	}

	passwordRef, _, err := unstructured.NestedStringMap(got[0].UnstructuredContent(), "spec", "chart", "credentials", "passwordRef")
	if err != nil {
		log.Printf("[ERR] resolving 'spec.chart.credentials.passwordRef': %s (%s@%s)\n", err.Error(), got[0].GetName(), got[0].GetNamespace())
		return nil, err
	}

	var password string
	if passwordRef != nil {
		password, err = GetSecret(context.Background(), g.dynamicClient, SecretKeySelector{
			Name:      passwordRef["name"],
			Namespace: passwordRef["namespace"],
			Key:       passwordRef["key"],
		})
		if err != nil {
			log.Printf("[ERR] resolving secret: %s (%s@%s)\n", err.Error(), passwordRef["name"], passwordRef["namespace"])
			return nil, err
		}
	}
	insecureSkipTLSverify, _, err := unstructured.NestedBool(got[0].UnstructuredContent(), "spec", "chart", "insecureSkipTLSverify")
	if err != nil {
		log.Printf("[ERR] resolving 'spec.chart.insecureSkipTLSverify': %s (%s@%s)\n", err.Error(), got[0].GetName(), got[0].GetNamespace())
		return nil, err
	}

	return &Info{
		URL:     packageUrl,
		Version: packageVersion,
		Repo:    repo,
		RegistryAuth: &helmclient.RegistryAuth{
			Username:              username,
			Password:              password,
			InsecureSkipTLSverify: insecureSkipTLSverify,
		},
	}, nil
}

type SecretKeySelector struct {
	Name      string
	Namespace string
	Key       string
}

func GetSecret(ctx context.Context, client dynamic.Interface, secretKeySelector SecretKeySelector) (string, error) {
	gvr := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	sec, err := client.Resource(gvr).Namespace(secretKeySelector.Namespace).Get(ctx, secretKeySelector.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	data, _, err := unstructured.NestedMap(sec.Object, "data")
	if err != nil {
		return "", err
	}
	bsec := data[secretKeySelector.Key].(string)
	bkey, err := base64.StdEncoding.DecodeString(bsec)
	if err != nil {
		return "", fmt.Errorf("failed to decode secret key: %w", err)
	}
	return string(bkey), nil
}
