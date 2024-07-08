package helmchart

import (
	"context"
	"fmt"
	"testing"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/client/helmclient"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/meta"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	pkgURL = "../../../testdata/dummy-chart-0.2.0.tgz"
)

func ExampleExtractValuesFromSpec() {
	res := createDummyResource()

	dat, err := ExtractValuesFromSpec(res)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(dat))
	// Output:
	// data:
	//   counter: 1
	//   greeting: Hello World!
	//   like: false
}

func TestRenderTemplate(t *testing.T) {
	res := createDummyResource()

	cli, err := connect(&zerolog.Logger{}, res)
	if err != nil {
		t.Fatal(err)
	}

	opts := RenderTemplateOptions{
		PackageUrl: "oci://registry-1.docker.io/bitnamicharts/postgresql",
		HelmClient: cli,
		Resource:   res,
	}

	all, err := RenderTemplate(context.TODO(), opts)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, "v1", all[0].APIVersion)
	assert.Equal(t, "Secret", all[0].Kind)
	assert.Equal(t, "demo-postgresql", all[0].Name)
	assert.Equal(t, "demo-system", all[0].Namespace)

	assert.Equal(t, "v1", all[1].APIVersion)
	assert.Equal(t, "Service", all[1].Kind)
	assert.Equal(t, "demo-postgresql-hl", all[1].Name)
	assert.Equal(t, "demo-system", all[1].Namespace)
}

func createDummyResource() *unstructured.Unstructured {
	data :=
		map[string]interface{}{
			"like":     false,
			"greeting": "Hello World!",
			"counter":  int64(1),
		}

	res := &unstructured.Unstructured{}
	res.SetGroupVersionKind(schema.FromAPIVersionAndKind("dummy-charts.krateo.io/v0-2-0", "DummyChart"))
	res.SetName("demo")
	res.SetNamespace("demo-system")
	unstructured.SetNestedField(res.Object, data, "spec", "data")

	return res
}

func connect(logger *zerolog.Logger, cr *unstructured.Unstructured) (helmclient.Client, error) {
	opts := &helmclient.Options{
		Namespace:        cr.GetNamespace(),
		RepositoryCache:  "/tmp/.helmcache",
		RepositoryConfig: "/tmp/.helmrepo",
		Debug:            true,
		Linting:          false,
		DebugLog: func(format string, v ...interface{}) {
			if !meta.IsVerbose(cr) {
				return
			}
			if len(v) > 0 {
				logger.Debug().Msgf(format, v)
			} else {
				logger.Debug().Msg(format)
			}
		},
	}

	return helmclient.New(opts)
}
