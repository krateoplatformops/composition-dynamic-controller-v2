package composition

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/gobuffalo/flect"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/client/restclient"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/controller"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/meta"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/text"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/apiaction"
	getter "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/restclient"
	"github.com/lucasepe/httplib"

	"github.com/rs/zerolog"

	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools"
	unstructuredtools "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var _ controller.ExternalClient = (*handler)(nil)

func NewHandler(cfg *rest.Config, log *zerolog.Logger, swg getter.Getter) controller.ExternalClient {
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
		swaggerInfoGetter: swg,
	}
}

type handler struct {
	logger            *zerolog.Logger
	dynamicClient     dynamic.Interface
	discoveryClient   *discovery.DiscoveryClient
	swaggerInfoGetter getter.Getter
}

func (h *handler) Observe(ctx context.Context, mg *unstructured.Unstructured) (bool, error) {
	log := h.logger.With().Timestamp().
		Str("op", "Observe").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	if h.swaggerInfoGetter == nil {
		return false, fmt.Errorf("swagger file info getter must be specified")
	}
	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting REST client info")
		return false, err
	}
	if clientInfo == nil {
		return false, fmt.Errorf("swagger info is nil")
	}

	for _, ownerRef := range clientInfo.OwnerReferences {
		ref, err := resolveObjectFromReferenceInfo(ownerRef, mg, h.dynamicClient)
		if err != nil {
			log.Err(err).Msg("Resolving reference")
			return false, err
		}
		mg.SetOwnerReferences([]metav1.OwnerReference{
			{
				APIVersion: ref.GetAPIVersion(),
				Kind:       ref.GetKind(),
				Name:       ref.GetName(),
				UID:        ref.GetUID(),
			},
		})
	}

	tools.Update(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})

	cli, err := restclient.BuildClient(clientInfo.URL)
	if err != nil {
		log.Err(err).Msg("Building REST client")
		return false, err
	}
	cli.Auth = clientInfo.Auth
	cli.Verbose = meta.IsVerbose(mg)
	cli.IdentifierFields = clientInfo.Resource.Identifiers
	cli.SpecFields = mg
	specFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		log.Err(err).Msg("Getting spec")
		return false, err
	}
	statusFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "status")
	if err != nil {
		log.Warn().AnErr("Getting status", err)
		// return false, nil
	}
	var body *map[string]interface{}
	isKnown := false
	// If status is empty, the resource is not created yet.
	for _, identifier := range clientInfo.Resource.Identifiers {
		if statusFields[identifier] != nil {
			isKnown = true
			break
		}
	}
	if isKnown {
		// Getting the external resource by its identifier
		apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Get)
		if err != nil {
			log.Err(err).Msg("Building API call")
			return false, err
		}
		reqConfiguration := BuildCallConfig(callInfo, statusFields, specFields)
		if reqConfiguration == nil {
			return false, fmt.Errorf("error building call configuration")
		}
		body, err = apiCall(ctx, http.DefaultClient, callInfo.Path, reqConfiguration)
		if httplib.IsNotFoundError(err) {
			log.Debug().Str("Resource", mg.GetKind()).Msg("External resource not found.")
			return false, nil
		}
		if err != nil {
			log.Err(err).Msg("Performing REST call")
			return false, err
		}
		if body == nil {
			return false, fmt.Errorf("response body is nil")
		}
	} else {
		apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.FindBy)
		if err != nil {
			log.Err(err).Msg("Building API call")
			return false, err
		}
		reqConfiguration := BuildCallConfig(callInfo, statusFields, specFields)
		if reqConfiguration == nil {
			return false, fmt.Errorf("error building call configuration")
		}
		body, err = apiCall(ctx, http.DefaultClient, callInfo.Path, reqConfiguration)
		if httplib.IsNotFoundError(err) {
			log.Debug().Str("Resource", mg.GetKind()).Msg("External resource not found.")
			return false, nil
		}
		if err != nil {
			log.Err(err).Msg("Performing REST call")
			return false, err
		}
		if body == nil {
			return false, fmt.Errorf("response body is nil")
		}
	}

	for k, v := range *body {
		for _, identifier := range clientInfo.Resource.Identifiers {
			if k == identifier {
				err = unstructured.SetNestedField(mg.Object, text.GenericToString(v), "status", identifier)
				if err != nil {
					log.Err(err).Msg("Setting identifier")
					return false, err
				}
				break
			}
		}
	}
	err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
	if err != nil {
		log.Err(err).Msg("Updating status")
		return false, err
	}

	ok, err := isCRUpdated(clientInfo.Resource, mg, *body)
	if err != nil {
		log.Err(err).Msg("Checking if CR is updated")
		return false, err
	}
	if !ok {
		log.Debug().Str("Resource", mg.GetKind()).Msg("External resource not up-to-date.")
		return true, apierrors.NewNotFound(schema.GroupResource{
			Group:    mg.GroupVersionKind().Group,
			Resource: flect.Pluralize(strings.ToLower(mg.GetKind())),
		}, mg.GetName())
	}

	log.Debug().Str("Resource", mg.GetKind()).Msg("External resource up-to-date.")

	return true, nil // apierrors.NewNotFound(schema.GroupResource{
	//		Group:    mg.GroupVersionKind().Group,
	//		Resource: flect.Pluralize(strings.ToLower(mg.GetKind())),
	//	}, mg.GetName())
}

func (h *handler) Create(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.With().Timestamp().
		Str("op", "Create").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting REST client info")
		return err
	}

	cli, err := restclient.BuildClient(clientInfo.URL)
	if err != nil {
		log.Err(err).Msg("Building REST client")
		return err
	}
	cli.Auth = clientInfo.Auth
	cli.Verbose = meta.IsVerbose(mg)

	specFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		log.Err(err).Msg("Getting spec")
		return err
	}
	apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Create)
	if err != nil {
		log.Err(err).Msg("Building API call")
		return err
	}
	reqConfiguration := BuildCallConfig(callInfo, nil, specFields)
	body, err := apiCall(ctx, http.DefaultClient, callInfo.Path, reqConfiguration)
	if err != nil {
		log.Err(err).Msg("Performing REST call")
		return err
	}
	if body == nil {
		return fmt.Errorf("response body is nil")
	}

	for k /* ,v */ := range *body {
		for _, identifier := range clientInfo.Resource.Identifiers {
			if k == identifier {
				// err = unstructured.SetNestedField(mg.Object, text.GenericToString(v), "status", identifier)
				err = unstructured.SetNestedField(mg.Object, nil, "status")
				if err != nil {
					log.Err(err).Msg("Setting identifier")
					return err
				}
			}
		}
	}

	log.Debug().Str("Resource", mg.GetKind()).Msg("Creating external resource.")

	err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
	if err != nil {
		log.Err(err).Msg("Updating status")
		return err
	}

	return nil
}

func (h *handler) Update(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.With().
		Str("op", "Update").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	log.Debug().Msg("Handling composition values update.")
	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting REST client info")
		return err
	}

	cli, err := restclient.BuildClient(clientInfo.URL)
	if err != nil {
		log.Err(err).Msg("Building REST client")
		return err
	}
	cli.Auth = clientInfo.Auth
	cli.Verbose = meta.IsVerbose(mg)

	specFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		log.Err(err).Msg("Getting spec")
		return err
	}
	apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Update)
	if err != nil {
		log.Err(err).Msg("Building API call")
		return err
	}

	statusFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "status")
	if err == fmt.Errorf("%s not found", "status") {
		log.Debug().Str("Resource", mg.GetKind()).Msg("External resource not created yet.")
		return err
	}
	reqConfiguration := BuildCallConfig(callInfo, statusFields, specFields)
	body, err := apiCall(ctx, http.DefaultClient, callInfo.Path, reqConfiguration)
	if err != nil {
		log.Err(err).Msg("Performing REST call")
		return err
	}
	if body == nil {
		return fmt.Errorf("response body is nil")
	}

	for k, v := range *body {
		for _, identifier := range clientInfo.Resource.Identifiers {
			if k == identifier {
				err = unstructured.SetNestedField(mg.Object, text.GenericToString(v), "status", identifier)
				if err != nil {
					log.Err(err).Msg("Setting identifier")
					return err
				}
			}
		}
	}

	log.Debug().Str("Resource", mg.GetKind()).Msg("Creating external resource.")

	err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
	if err != nil {
		log.Err(err).Msg("Updating status")
		return err
	}
	log.Debug().Str("kind", mg.GetKind()).Msg("Composition values updated.")

	return nil

}

func (h *handler) Delete(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.With().
		Str("op", "Delete").
		Str("apiVersion", mg.GetAPIVersion()).
		Str("kind", mg.GetKind()).
		Str("name", mg.GetName()).
		Str("namespace", mg.GetNamespace()).Logger()

	log.Debug().Msg("Handling composition values deletion.")

	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Err(err).Msg("Getting REST client info")
		return err
	}

	cli, err := restclient.BuildClient(clientInfo.URL)
	if err != nil {
		log.Err(err).Msg("Building REST client")
		return err
	}
	cli.Auth = clientInfo.Auth
	cli.Verbose = true

	specFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		log.Err(err).Msg("Getting spec")
		return err
	}
	statusFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "status")
	if err != nil {
		log.Err(err).Msg("Getting status")
		return err
	}
	apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Delete)
	if err != nil {
		log.Err(err).Msg("Building API call")
		return err
	}
	reqConfiguration := BuildCallConfig(callInfo, statusFields, specFields)
	if reqConfiguration == nil {
		return fmt.Errorf("error building call configuration")
	}

	_, err = apiCall(ctx, http.DefaultClient, callInfo.Path, reqConfiguration)
	if err != nil {
		log.Err(err).Msg("Performing REST call")
		return err
	}

	log.Debug().Str("Resource", mg.GetKind()).Msg("Deleting external resource.")

	// Deleting finalizer
	mg.SetFinalizers([]string{})
	err = tools.Update(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: h.discoveryClient,
		DynamicClient:   h.dynamicClient,
	})
	if err != nil {
		log.Err(err).Msg("Deleting finalizer")
		return err
	}

	return nil
}
