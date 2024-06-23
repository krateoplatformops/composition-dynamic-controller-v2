package helmchart

import (
	"context"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/helmclient"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type InstallOptions struct {
	HelmClient helmclient.Client
	ChartName  string
	Resource   *unstructured.Unstructured
}

func Install(ctx context.Context, opts InstallOptions) (*release.Release, int64, error) {
	chartSpec := helmclient.ChartSpec{
		ReleaseName:     opts.Resource.GetName(),
		Namespace:       opts.Resource.GetNamespace(),
		ChartName:       opts.ChartName,
		CreateNamespace: true,
		UpgradeCRDs:     true,
		Wait:            false,
	}

	dat, err := ExtractValuesFromSpec(opts.Resource)
	if err != nil {
		return nil, 0, err
	}
	if len(dat) == 0 {
		return nil, 0, nil
	}

	uid := opts.Resource.GetUID()
	claimGen := opts.Resource.GetGeneration()
	chartSpec.ValuesYaml = string(dat)

	helmOpts := &helmclient.GenericHelmOptions{
		PostRenderer: &labelsPostRender{
			UID: uid,
		},
	}
	rel, err := opts.HelmClient.InstallOrUpgradeChart(ctx, &chartSpec, helmOpts)
	return rel, claimGen, err
}
