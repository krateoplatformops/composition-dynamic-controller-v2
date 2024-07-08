package helmchart

import (
	"context"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/client/helmclient"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Credentials struct {
	Username string
	Password string
}

type InstallOptions struct {
	HelmClient  helmclient.Client
	ChartName   string
	Resource    *unstructured.Unstructured
	Repo        string
	Version     string
	Credentials *Credentials
}

func Install(ctx context.Context, opts InstallOptions) (*release.Release, int64, error) {
	chartSpec := helmclient.ChartSpec{
		ReleaseName:     opts.Resource.GetName(),
		Namespace:       opts.Resource.GetNamespace(),
		Version:         opts.Version,
		Repo:            opts.Repo,
		ChartName:       opts.ChartName,
		CreateNamespace: true,
		UpgradeCRDs:     true,
		Wait:            false,
	}
	if opts.Credentials != nil {
		chartSpec.Username = opts.Credentials.Username
		chartSpec.Password = opts.Credentials.Password
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
