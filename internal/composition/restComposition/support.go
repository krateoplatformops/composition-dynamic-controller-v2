package composition

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/gobuffalo/flect"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/client/restclient"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/text"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/apiaction"
	getter "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/restclient"
	unstructuredtools "github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

type RequestedParams struct {
	Parameters text.StringSet
	Query      text.StringSet
	Body       text.StringSet
}

type CallInfo struct {
	Path             string
	ReqParams        *RequestedParams
	IdentifierFields []string
	AltFields        map[string]string
}

type APIFuncDef func(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (*map[string]interface{}, error)

// APICallBuilder builds the API call based on the action and the info from the RestDefinition
func APICallBuilder(cli *restclient.UnstructuredClient, info *getter.Info, action apiaction.APIAction) (apifunc APIFuncDef, callInfo *CallInfo, err error) {
	identifierFields := info.Resource.Identifiers
	for _, descr := range info.Resource.VerbsDescription {
		if strings.EqualFold(descr.Action, action.String()) {
			method, err := restclient.StringToApiCallType(descr.Method)
			if action == apiaction.FindBy {
				method = restclient.APICallsTypeFindBy
			}
			if err != nil {
				return nil, nil, fmt.Errorf("error converting method to api call type: %s", err)
			}
			params, query, err := cli.RequestedParams(descr.Method, descr.Path)
			if err != nil {
				return nil, nil, fmt.Errorf("error retrieving requested params: %s", err)
			}
			var body text.StringSet
			if descr.Method == "POST" || descr.Method == "PUT" || descr.Method == "PATCH" {
				body, err = cli.RequestedBody(descr.Method, descr.Path)
				if err != nil {
					return nil, nil, fmt.Errorf("error retrieving requested body params: %s", err)
				}
				if body == nil {
					body = text.StringSet{}
				}
			}

			callInfo := &CallInfo{
				Path: descr.Path,
				ReqParams: &RequestedParams{
					Parameters: params,
					Query:      query,
					Body:       body,
				},
				AltFields:        descr.AltFieldMapping,
				IdentifierFields: identifierFields,
			}
			switch method {
			case restclient.APICallsTypeGet:
				return cli.Get, callInfo, nil
			case restclient.APICallsTypePost:
				return cli.Post, callInfo, nil
			case restclient.APICallsTypeList:
				return cli.List, callInfo, nil
			case restclient.APICallsTypeDelete:
				return cli.Delete, callInfo, nil
			case restclient.APICallsTypePatch:
				return cli.Patch, callInfo, nil
			case restclient.APICallsTypeFindBy:
				return cli.FindBy, callInfo, nil
			case restclient.APICallsTypePut:
				return cli.Put, callInfo, nil
			}
		}
	}
	return nil, nil, nil //fmt.Errorf("impossible to build api call for action %s", action.String())
}

// BuildCallConfig builds the request configuration based on the callInfo and the fields from the status and spec
func BuildCallConfig(callInfo *CallInfo, statusFields map[string]interface{}, specFields map[string]interface{}) *restclient.RequestConfiguration {
	reqConfiguration := &restclient.RequestConfiguration{}
	reqConfiguration.Parameters = make(map[string]string)
	reqConfiguration.Query = make(map[string]string)
	mapBody := make(map[string]interface{})

	processFields(callInfo, specFields, reqConfiguration, mapBody)
	processFields(callInfo, statusFields, reqConfiguration, mapBody)
	reqConfiguration.Body = mapBody
	return reqConfiguration
}

func processFields(callInfo *CallInfo, fields map[string]interface{}, reqConfiguration *restclient.RequestConfiguration, mapBody map[string]interface{}) {
	for field, value := range fields {
		// fmt.Println("Processing field: ", field, value)
		field, value = processAltFields(callInfo, field, value)
		// fmt.Println("Field: ", field, value)
		if field == "" {
			continue
		}
		if callInfo.ReqParams.Parameters.Contains(field) {
			stringVal := fmt.Sprintf("%v", value)
			if stringVal == "" && reqConfiguration.Parameters[field] != "" {
				continue
			}
			reqConfiguration.Parameters[field] = stringVal
		} else if callInfo.ReqParams.Query.Contains(field) {
			stringVal := fmt.Sprintf("%v", value)
			if stringVal == "" && reqConfiguration.Query[field] != "" {
				continue
			}
			reqConfiguration.Query[field] = stringVal
		} else if callInfo.ReqParams.Body.Contains(field) {
			mapBody[field] = value
		}
	}
}

// if there are alternative fields, we need to check if the field is in the alternative field mapping
func processAltFields(callInfo *CallInfo, field string, value interface{}) (string, interface{}) {
	val := value
	for new, old := range callInfo.AltFields {
		if old == field {
			// fmt.Println("Check before processing: ", new, old)
			split := strings.Split(new, ".")
			for i, altf := range split {
				if strings.Contains(altf, "[]") {
					arrayVal, ok := val.([]interface{})
					if !ok {
						continue
					}
					strVal := ""
					for _, value := range arrayVal {
						// fmt.Println("len: ", len(split), i)
						_, v := processAltFields(callInfo, split[i+1], value)
						strv, ok := v.(string)
						if !ok {
							continue
						}
						strVal += strv
						strVal += ","
						// fmt.Println("After recursive call: ", f, strVal)
					}
					strVal = strings.TrimSuffix(strVal, ",")
					val = strVal
				} else {
					mapval, ok := val.(map[string]interface{})
					if ok {
						nval, ok := mapval[altf]
						if ok {
							val = nval
						}
					}
				}
			}
			if !reflect.DeepEqual(val, value) {
				// fmt.Println("Returning: ", old, val)
				return old, val
			}
		}
	}

	f, ok := callInfo.AltFields[field]
	if ok {
		field = f
	}
	// fmt.Println("Check after processing: ", field, val)
	return field, value
}

// resolveObjectFromReferenceInfo resolves the object from the reference info, used by OwnerReference
func resolveObjectFromReferenceInfo(ref getter.ReferenceInfo, mg *unstructured.Unstructured, dyClient dynamic.Interface) (*unstructured.Unstructured, error) {
	gvrForReference := schema.GroupVersionResource{
		Group:    ref.GroupVersionKind.Group,
		Version:  ref.GroupVersionKind.Version,
		Resource: strings.ToLower(flect.Pluralize(ref.GroupVersionKind.Kind)),
	}

	all, err := dyClient.Resource(gvrForReference).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting reference resource - %w", err)
	}
	if len(all.Items) == 0 {
		return nil, fmt.Errorf("no reference found for resource %s - len is zero", gvrForReference.Resource)
	}

	fieldValue, ok, err := unstructured.NestedString(mg.Object, "spec", ref.Field)
	if !ok {
		return nil, fmt.Errorf("spec field %s not found in reference resource", ref.Field)
	}
	if err != nil {
		return nil, fmt.Errorf("error getting spec field %s from reference resource - %w", ref.Field, err)
	}

	for _, item := range all.Items {
		statusMap, ok, err := unstructured.NestedMap(item.Object, "status")
		if !ok {
			return nil, fmt.Errorf("status field not found in reference resource")
		}
		if err != nil {
			return nil, fmt.Errorf("error getting status field from reference resource - %w", err)
		}

		for _, v := range statusMap {
			strField, ok := v.(string)
			if ok {
				if strField == fieldValue {
					return &item, nil
				}
			}
		}

		specMap, ok, err := unstructured.NestedMap(item.Object, "spec")
		if !ok {
			return nil, fmt.Errorf("spec field not found in reference resource")
		}
		if err != nil {
			return nil, fmt.Errorf("error getting spec field from reference resource - %w", err)
		}
		for _, v := range specMap {
			strField, ok := v.(string)
			if ok {
				if strField == fieldValue {
					return &item, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no reference found for resource %s", gvrForReference.Resource)
}

// isCRUpdated checks if the CR was updated by comparing the fields in the CR with the response from the API call, if existing cr fields are different from the response, it returns false
func isCRUpdated(def getter.Resource, mg *unstructured.Unstructured, rm map[string]interface{}) (bool, error) {
	specs, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		return false, fmt.Errorf("error getting spec fields: %w", err)
	}
	if len(def.CompareList) > 0 {
		for _, field := range def.CompareList {
			if _, ok := rm[field]; !ok {
				return false, fmt.Errorf("field %s not found in response", field)
			}
			if !reflect.DeepEqual(specs[field], rm[field]) {
				return false, nil
			}
		}
		return true, nil
	}

	m, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		return false, fmt.Errorf("error getting spec fields: %w", err)
	}

	return compareExisting(m, rm), nil
}

// compareExisting recursively compares fields between two maps and logs differences.
func compareExisting(mg map[string]interface{}, rm map[string]interface{}, path ...string) bool {
	for key, value := range mg {
		currentPath := append(path, key)
		pathStr := fmt.Sprintf("%v", currentPath)

		rmValue, ok := rm[key]
		if !ok {
			continue
		}

		switch reflect.TypeOf(value).Kind() {
		case reflect.Map:
			mgMap, ok1 := value.(map[string]interface{})
			if !ok1 {
				fmt.Printf("Type assertion failed for map at '%s'\n", pathStr)
				continue
			}
			rmMap, ok2 := rmValue.(map[string]interface{})
			if !ok2 {
				fmt.Printf("Type assertion failed for map at '%s'\n", pathStr)
				continue
			}
			if !compareExisting(mgMap, rmMap, currentPath...) {
				fmt.Printf("Values differ at '%s'\n", pathStr)
				return false
			}
		case reflect.Slice:
			valueSlice, ok1 := value.([]interface{})
			if !ok1 || reflect.TypeOf(rmValue).Kind() != reflect.Slice {
				fmt.Printf("Values are not both slices or type assertion failed at '%s'\n", pathStr)
				continue
			}
			rmSlice, ok2 := rmValue.([]interface{})
			if !ok2 {
				fmt.Printf("Type assertion failed for slice at '%s'\n", pathStr)
				continue
			}
			for i, v := range valueSlice {
				if reflect.TypeOf(v).Kind() == reflect.Map {
					mgMap, ok1 := v.(map[string]interface{})
					if !ok1 {
						fmt.Printf("Type assertion failed for map at '%s'\n", pathStr)
						continue
					}
					rmMap, ok2 := rmSlice[i].(map[string]interface{})
					if !ok2 {
						fmt.Printf("Type assertion failed for map at '%s'\n", pathStr)
						continue
					}
					if !compareExisting(mgMap, rmMap, currentPath...) {
						fmt.Printf("Values differ at '%s'\n", pathStr)
						return false
					}
				} else if v != rmSlice[i] {
					fmt.Printf("Values differ at '%s'\n", pathStr)
					return false
				}
			}
		default:
			if !compareAny(value, rmValue) {
				fmt.Printf("Values differ at '%s' %s %s\n", pathStr, value, rmValue)
				return false
			}
		}
	}

	return true
}
func numberCaster(value interface{}) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v // No conversion needed since v is already int64
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	case float32:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return -999999 // Return a default value if none of the cases match
	}
}

func compareAny(a any, b any) bool {
	//if is number compare as number
	switch a.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		ia := numberCaster(a)
		ib := numberCaster(b)
		return ia == ib
	case string:
		sa := a.(string)
		sb := b.(string)
		return sa == sb
	case bool:
		ba := a.(bool)
		bb := b.(bool)
		return ba == bb
	default:
		return reflect.DeepEqual(a, b)
	}
}

func removeFinalizersAndUpdate(ctx context.Context, log zerolog.Logger, discovery *discovery.DiscoveryClient, dynamic dynamic.Interface, mg *unstructured.Unstructured) error {
	mg.SetFinalizers([]string{})
	err := tools.Update(ctx, mg, tools.UpdateOptions{
		DiscoveryClient: discovery,
		DynamicClient:   dynamic,
	})
	if err != nil {
		log.Err(err).Msg("Deleting finalizer")
		return err
	}
	return nil
}

// populateStatusFields populates the status fields in the mg object with the values from the body
func populateStatusFields(clientInfo *getter.Info, mg *unstructured.Unstructured, body *map[string]interface{}) error {
	if body != nil {
		for k, v := range *body {
			for _, identifier := range clientInfo.Resource.Identifiers {
				if k == identifier {
					err := unstructured.SetNestedField(mg.Object, text.GenericToString(v), "status", identifier)
					if err != nil {
						log.Err(err).Msg("Setting identifier")
						return err
					}
				}
			}
		}
	}
	return nil
}

// tries to find the resource in the cluster, with the given statusFields and specFields values, if it is able to validate the GET request, returns true
func isResourceKnown(cli *restclient.UnstructuredClient, log zerolog.Logger, clientInfo *getter.Info, statusFields map[string]interface{}, specFields map[string]interface{}) bool {
	apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Get)
	if apiCall == nil {
		return false
	}
	if err != nil {
		log.Err(err).Msg("Building API call")
		return false
	}
	reqConfiguration := BuildCallConfig(callInfo, statusFields, specFields)
	if reqConfiguration == nil {
		return false
	}

	actionGetMethod := "GET"
	for _, descr := range clientInfo.Resource.VerbsDescription {
		if strings.EqualFold(descr.Action, apiaction.Get.String()) {
			actionGetMethod = descr.Method
		}
	}

	if cli.ValidateRequest(actionGetMethod, callInfo.Path, reqConfiguration.Parameters, reqConfiguration.Query) != nil {
		return false
	}
	return true
}
