package helmchart

import (
	"context"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/client/helmclient"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type UpdateOptions struct {
	HelmClient  helmclient.Client
	ChartName   string
	Version     string
	Resource    *unstructured.Unstructured
	Repo        string
	Credentials *Credentials
}

func Update(ctx context.Context, opts UpdateOptions) error {
	chartSpec := helmclient.ChartSpec{
		ReleaseName:     opts.Resource.GetName(),
		Namespace:       opts.Resource.GetNamespace(),
		ChartName:       opts.ChartName,
		Version:         opts.Version,
		CreateNamespace: true,
		UpgradeCRDs:     true,
		Replace:         true,
	}

	dat, err := ExtractValuesFromSpec(opts.Resource)
	if err != nil {
		return err
	}
	if len(dat) > 0 {
		chartSpec.ResetValues = true
		chartSpec.ValuesYaml = string(dat)
	}

	_, err = opts.HelmClient.UpgradeChart(ctx, &chartSpec, nil)
	return err
}
