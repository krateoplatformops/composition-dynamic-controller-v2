package unstructured

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/gobuffalo/flect"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/controller"
	"github.com/krateoplatformops/composition-dynamic-controller/internal/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type NotAvailableError struct {
	FailedObjectRef *controller.ObjectRef
	Err             error
}

func (r *NotAvailableError) Error() string {
	if r.FailedObjectRef == nil {
		return fmt.Sprintf("err %v", r.Err)
	}
	return fmt.Sprintf("failedObjectRef %s: err %v", r.FailedObjectRef, r.Err)
}

func IsAvailable(un *unstructured.Unstructured) (bool, error) {
	positives := []string{
		"ready", "complete", "healthy", "active", "able",
	}

	conds := GetConditions(un)
	if len(conds) == 0 {
		return true, nil
	}

	for _, co := range conds {
		if has(positives, string(co.Type)) {
			if string(co.Status) != "True" {
				return false, &NotAvailableError{
					FailedObjectRef: &controller.ObjectRef{
						APIVersion: un.GetAPIVersion(),
						Kind:       un.GetKind(),
						Name:       un.GetName(),
						Namespace:  un.GetNamespace(),
					},
					Err: fmt.Errorf(co.Reason),
				}
			}
		}
	}

	return true, nil
}

func SetCondition(un *unstructured.Unstructured, co metav1.Condition) error {
	conds := GetConditions(un)
	condition.Upsert(&conds, co)

	res, err := encodeStruct(conds)
	if err != nil {
		return err
	}

	return unstructured.SetNestedField(un.Object, res, "status", "conditions")
}

// GetConditions returns the conditions, excluding the `message` field.
func GetConditions(un *unstructured.Unstructured) []metav1.Condition {
	if un == nil {
		return nil
	}
	items, _, _ := unstructured.NestedSlice(un.Object, "status", "conditions")
	x := []metav1.Condition{}
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		_, ok = m["type"].(string)
		if !ok {
			return nil
		}
		_, ok = m["status"].(string)
		if !ok {
			return nil
		}
		x = append(x, metav1.Condition{
			Type:   m["type"].(string),
			Status: metav1.ConditionStatus(m["status"].(string)),
			Reason: m["reason"].(string),
		})
	}
	return x
}

func IsConditionSet(un *unstructured.Unstructured, cond metav1.Condition) bool {
	conds := GetConditions(un)
	for _, co := range conds {
		if co.Type == cond.Type && co.Status == cond.Status && co.Reason == cond.Reason {
			return true
		}
	}
	return false
}

func SetFailedObjectRef(un *unstructured.Unstructured, ref *controller.ObjectRef) error {
	return setNestedFieldNoCopy(un, map[string]interface{}{
		"apiVersion": ref.APIVersion,
		"kind":       ref.Kind,
		"name":       ref.Name,
		"namespace":  ref.Namespace,
	}, "status", "failedObjectRef")
}

func ExtractFailedObjectRef(un *unstructured.Unstructured) (*controller.ObjectRef, error) {
	obj, ok, err := unstructured.NestedFieldNoCopy(un.Object, "status", "failedObjectRef")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	dat, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	ref := &controller.ObjectRef{}
	err = json.Unmarshal(dat, ref)
	return ref, err
}

func UnsetFailedObjectRef(un *unstructured.Unstructured) {
	removeNestedField(un, "status", "failedObjectRef")
}

func GVR(un *unstructured.Unstructured) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(un.GetAPIVersion())
	if err != nil {
		return schema.GroupVersionResource{}, err
	}

	resource := strings.ToLower(flect.Pluralize(un.GetKind()))
	return gv.WithResource(resource), nil
}

func setNestedFieldNoCopy(uns *unstructured.Unstructured, value interface{}, fields ...string) error {
	m := uns.Object

	for i, field := range fields[:len(fields)-1] {
		if val, ok := m[field]; ok {
			if valMap, ok := val.(map[string]interface{}); ok {
				m = valMap
			} else {
				return fmt.Errorf("value cannot be set because %v is not a map[string]interface{}", fields[:i+1])
			}
		} else {
			newVal := make(map[string]interface{})
			m[field] = newVal
			m = newVal
		}
	}
	m[fields[len(fields)-1]] = value
	return nil
}

// removeNestedField removes the nested field from the obj.
func removeNestedField(uns *unstructured.Unstructured, fields ...string) {
	m := uns.Object
	for _, field := range fields[:len(fields)-1] {
		if x, ok := m[field].(map[string]interface{}); ok {
			m = x
		} else {
			return
		}
	}
	delete(m, fields[len(fields)-1])
}

// has checks if a string is present in a slice
func has(s []string, str string) bool {
	for _, v := range s {
		if strings.Contains(strings.ToLower(str), v) {
			return true
		}
	}

	return false
}

// Converts a struct to a map while maintaining the json alias as keys
func encodeStruct(obj interface{}) (res interface{}, err error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return
	}

	err = json.Unmarshal(data, &res)
	return
}

func IsOnList(key string, value interface{}, list *unstructured.UnstructuredList) (*unstructured.Unstructured, error) {
	for _, item := range list.Items {
		field, ok, err := unstructured.NestedFieldNoCopy(item.Object, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if reflect.DeepEqual(field, value) {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("elem with key %s and value %s not found", key, value)
}

func GetFieldsFromUnstructured(u *unstructured.Unstructured, field string) (map[string]interface{}, error) {
	spec, ok, err := unstructured.NestedFieldNoCopy(u.Object, field)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s not found", field)
	}
	fields := make(map[string]interface{})
	if reflect.ValueOf(spec).CanInterface() {
		iter := reflect.ValueOf(spec).MapRange()
		for iter.Next() {
			k := iter.Key()
			v := iter.Value()
			fields[k.String()] = v.Interface()
		}
	}
	return fields, nil
}
