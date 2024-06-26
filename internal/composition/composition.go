package composition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/controller"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/helmclient"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/meta"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/helmchart"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/helmchart/archive"

	"github.com/rs/zerolog"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools"

	unstructuredtools "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured/condition"
)

var (
	errReleaseNotFound  = errors.New("helm release not found")
	errCreateIncomplete = "cannot determine creation result - remove the " + meta.AnnotationKeyExternalCreatePending + " annotation if it is safe to proceed"
)

var _ controller.ExternalClient = (*handler)(nil)

func NewHandler(cfg *rest.Config, log *zerolog.Logger, pig archive.Getter) controller.ExternalClient {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Creating dynamic client.")
	}

	dis, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Creating discovery client.")
	}

	return &handler{
		logger:            log,
		dynamicClient:     dyn,
		discoveryClient:   dis,
		packageInfoGetter: pig,
	}
}

type handler struct {
	logger            *zerolog.Logger
	dynamicClient     dynamic.Interface
	discoveryClient   *discovery.DiscoveryClient
	packageInfoGetter archive.Getter
}

func (h *handler) Observe(ctx context.Context, mg *unstructured.Unstructured) (bool, error) {
	log := h.logger.With().
		Str("op", "Observe").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	if h.packageInfoGetter == nil {
		return false, fmt.Errorf("helm chart package info getter must be specified")
	}

	pkg, err := h.packageInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting package info")
		return false, err
	}

	hc, err := h.helmClientForResource(mg, pkg.RegistryAuth)
	if err != nil {
		log.Err(err).Msg("Getting helm client")
		return false, err
	}

	rel, err := helmchart.FindRelease(hc, mg.GetName())
	if err != nil {
		if !errors.Is(err, errReleaseNotFound) {
			return false, err
		}
	}
	if rel == nil {
		log.Debug().Msg("Composition package not installed.")
		return false, nil
	}

	renderOpts := helmchart.RenderTemplateOptions{
		HelmClient:     hc,
		Resource:       mg,
		PackageUrl:     pkg.URL,
		PackageVersion: pkg.Version,
		Repo:           pkg.Repo,
	}
	if pkg.RegistryAuth != nil {
		renderOpts.Credentials = &helmchart.Credentials{
			Username: pkg.RegistryAuth.Username,
			Password: pkg.RegistryAuth.Password,
		}
	}

	all, err := helmchart.RenderTemplate(ctx, renderOpts)
	if err != nil {
		log.Err(err).Msg("Rendering helm chart template")
		return false, err
	}
	if len(all) == 0 {
		return true, nil
	}

	log.Debug().Str("package", pkg.URL).Msg("Checking composition resources.")

	opts := helmchart.CheckResourceOptions{
		DynamicClient:   h.dynamicClient,
		DiscoveryClient: h.discoveryClient,
	}

	for _, el := range all {
		log.Debug().Str("package", pkg.URL).Msgf("Checking for resource %s.", el.String())

		ref, err := helmchart.CheckResource(ctx, el, opts)
		if err != nil {
			if ref == nil {
				log.Warn().Err(err).
					Str("package", pkg.URL).
					Msgf("Composition not ready due to: %s.", el.String())
				return false, nil
			}

			_ = unstructuredtools.SetFailedObjectRef(mg, ref)
			_ = unstructuredtools.SetCondition(mg, condition.Unavailable())

			return true, tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				DiscoveryClient: h.discoveryClient,
				DynamicClient:   h.dynamicClient,
			})
		}
	}

	log.Debug().Str("package", pkg.URL).Msg("Composition ready.")

	if meta.ExternalCreateIncomplete(mg) {
		meta.SetExternalCreateSucceeded(mg, time.Now())
		return true, tools.Update(ctx, mg, tools.UpdateOptions{
			DiscoveryClient: h.discoveryClient,
			DynamicClient:   h.dynamicClient,
		})
	}

	// fmt.Println("Update status")
	_ = unstructuredtools.SetCondition(mg, condition.Available())
	err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
	if err != nil {
		log.Err(err).Msgf("Updating cr status with condition: %v", condition.Available())
	}

	return true, err
}

func (h *handler) Create(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.With().
		Str("op", "Create").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	if meta.ExternalCreateIncomplete(mg) {
		log.Warn().Msg(errCreateIncomplete)
		err := unstructuredtools.SetCondition(mg, condition.Creating())
		if err != nil {
			return err
		}
		return tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
			DiscoveryClient: h.discoveryClient,
			DynamicClient:   h.dynamicClient,
		})
	}

	if h.packageInfoGetter == nil {
		return fmt.Errorf("helm chart package info getter must be specified")
	}

	pkg, err := h.packageInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting package info")
		return err
	}

	hc, err := h.helmClientForResource(mg, pkg.RegistryAuth)
	if err != nil {
		log.Err(err).Msg("Getting helm client")
		return err
	}

	opts := helmchart.InstallOptions{
		HelmClient: hc,
		ChartName:  pkg.URL,
		Resource:   mg,
		Repo:       pkg.Repo,
		Version:    pkg.Version,
	}
	if pkg.RegistryAuth != nil {
		opts.Credentials = &helmchart.Credentials{
			Username: pkg.RegistryAuth.Username,
			Password: pkg.RegistryAuth.Password,
		}
	}

	_, _, err = helmchart.Install(ctx, opts)
	if err != nil {
		log.Err(err).Msgf("Installing helm chart: %s", pkg.URL)
		meta.SetExternalCreateFailed(mg, time.Now())
		_ = tools.Update(ctx, mg, tools.UpdateOptions{
			DiscoveryClient: h.discoveryClient,
			DynamicClient:   h.dynamicClient,
		})

		unstructuredtools.SetCondition(mg, condition.FailWithReason(
			fmt.Sprintf("Creating failed: %s", err.Error())))

		_ = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
			DiscoveryClient: h.discoveryClient,
			DynamicClient:   h.dynamicClient,
		})

		return err
	}

	log.Debug().Str("package", pkg.URL).Msg("Installing composition package.")

	meta.SetExternalCreatePending(mg, time.Now())
	return tools.Update(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
}

func (h *handler) Update(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.With().
		Str("op", "Update").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	log.Debug().Msg("Handling composition values update.")

	// If we started but never completed creation of an external resource we
	// may have lost critical information.The safest thing to
	// do is to refuse to proceed.
	if meta.ExternalCreateIncomplete(mg) {
		log.Warn().Msg(errCreateIncomplete)
		_ = unstructuredtools.SetCondition(mg, condition.Creating())

		return tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
			DiscoveryClient: h.discoveryClient,
			DynamicClient:   h.dynamicClient,
		})
	}

	meta.SetExternalCreatePending(mg, time.Now())
	err := tools.Update(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
	if err != nil {
		log.Err(err).Msg("Setting meta create pending annotation.")
		return err
	}

	if h.packageInfoGetter == nil {
		return fmt.Errorf("helm chart package info getter must be specified")
	}

	pkg, err := h.packageInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting package info")
		return err
	}

	hc, err := h.helmClientForResource(mg, pkg.RegistryAuth)
	if err != nil {
		log.Err(err).Msg("Getting helm client")
		return err
	}

	opts := helmchart.UpdateOptions{
		HelmClient: hc,
		ChartName:  pkg.URL,
		Resource:   mg,
		Repo:       pkg.Repo,
		Version:    pkg.Version,
	}
	if pkg.RegistryAuth != nil {
		opts.Credentials = &helmchart.Credentials{
			Username: pkg.RegistryAuth.Username,
			Password: pkg.RegistryAuth.Password,
		}
	}

	err = helmchart.Update(ctx, opts)
	if err != nil {
		log.Err(err).Msg("Performing helm chart update")
		return err
	}

	log.Debug().Str("package", pkg.URL).Msg("Composition values updated.")

	return nil
}

func (h *handler) Delete(ctx context.Context, ref controller.ObjectRef) error {
	if h.packageInfoGetter == nil {
		return fmt.Errorf("helm chart package info getter must be specified")
	}

	mg := unstructured.Unstructured{}
	mg.SetAPIVersion(ref.APIVersion)
	mg.SetKind(ref.Kind)
	mg.SetName(ref.Name)
	mg.SetNamespace(ref.Namespace)

	hc, err := h.helmClientForResource(&mg, nil)
	if err != nil {
		return err
	}

	pkg, err := h.packageInfoGetter.Get(&mg)
	if err != nil {
		return err
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName: mg.GetName(),
		Namespace:   mg.GetNamespace(),
		ChartName:   pkg.URL,
		Version:     pkg.Version,
		Wait:        true,
		Timeout:     time.Minute * 3,
	}

	err = hc.UninstallRelease(&chartSpec)
	if err != nil {
		return err
	}

	h.logger.Debug().Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).
		Str("package", pkg.URL).
		Msg("Composition package removed.")

	return nil
}

func (h *handler) helmClientForResource(mg *unstructured.Unstructured, registryAuth *helmclient.RegistryAuth) (helmclient.Client, error) {
	log := h.logger.With().
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	opts := &helmclient.Options{
		Namespace:        mg.GetNamespace(),
		RepositoryCache:  "/tmp/.helmcache",
		RepositoryConfig: "/tmp/.helmrepo",
		Debug:            true,
		Linting:          false,
		DebugLog: func(format string, v ...interface{}) {
			if !meta.IsVerbose(mg) {
				return
			}

			if len(v) > 0 {
				log.Debug().Msgf(format, v)
			} else {
				log.Debug().Msg(format)
			}
		},
		RegistryAuth: (registryAuth),
	}

	return helmclient.New(opts)
}
