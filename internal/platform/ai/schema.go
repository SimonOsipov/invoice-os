// schema.go validates the caller's schema subset and checks an answer
// against it.
package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var allowedSchemaTypes = map[string]bool{
	"object": true, "string": true, "number": true, "integer": true,
	"boolean": true, "array": true, "null": true,
}

// checkSchema parses and validates the caller's schema subset. It decodes
// with UseNumber so a schema enum's numbers are json.Number, the same type
// decodeAnswer produces, and so compare equal (TestCall_EnumOfNumbersComparesByJSONValue).
func checkSchema(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid schema JSON: %v", err)
	}
	if err := validateSchemaNode(m, true); err != nil {
		return nil, err
	}
	return m, nil
}

// validateSchemaNode checks one schema object; top applies the
// object-only rule that holds at the root but not for nested schemas.
func validateSchemaNode(node map[string]any, top bool) error {
	for key := range node {
		switch key {
		case "type", "properties", "required", "additionalProperties", "items", "enum", "description":
		default:
			return fmt.Errorf("disallowed schema keyword %q", key)
		}
	}

	if raw, ok := node["type"]; ok {
		switch t := raw.(type) {
		case string:
			if !allowedSchemaTypes[t] {
				return fmt.Errorf("invalid schema type %q", t)
			}
		case []any:
			for _, item := range t {
				s, ok := item.(string)
				if !ok || !allowedSchemaTypes[s] {
					return fmt.Errorf("invalid schema type %v", item)
				}
			}
		default:
			return errors.New("schema type must be a string or an array of strings")
		}
	}

	if top {
		s, ok := node["type"].(string)
		if !ok || s != "object" {
			return errors.New(`top-level schema type must be exactly "object"`)
		}
	}

	if raw, ok := node["additionalProperties"]; ok {
		b, ok := raw.(bool)
		if !ok || b {
			return errors.New("additionalProperties must be false when present")
		}
	}

	if raw, ok := node["properties"]; ok {
		props, ok := raw.(map[string]any)
		if !ok {
			return errors.New("properties must be an object")
		}
		for name, v := range props {
			child, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("properties.%s must be an object", name)
			}
			if err := validateSchemaNode(child, false); err != nil {
				return err
			}
		}
	}

	if raw, ok := node["items"]; ok {
		child, ok := raw.(map[string]any)
		if !ok {
			return errors.New("items must be an object")
		}
		if err := validateSchemaNode(child, false); err != nil {
			return err
		}
	}

	return nil
}

// checkAnswer checks v against schema, recursing into properties and items.
func checkAnswer(schema map[string]any, v any) error {
	if typs := jsonTypes(schema["type"]); len(typs) > 0 && !matchesAnyType(typs, v) {
		return fmt.Errorf("value does not match type %v", typs)
	}

	if rawEnum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, item := range rawEnum {
			if equalJSON(item, v) {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("value is not in enum")
		}
	}

	switch vv := v.(type) {
	case map[string]any:
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				key, _ := r.(string)
				if _, present := vv[key]; !present {
					return fmt.Errorf("missing required key %q", key)
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		if addl, ok := schema["additionalProperties"].(bool); ok && !addl {
			for key := range vv {
				if _, known := props[key]; !known {
					return fmt.Errorf("unexpected key %q", key)
				}
			}
		}
		for key, propSchema := range props {
			val, present := vv[key]
			if !present {
				continue
			}
			ps, _ := propSchema.(map[string]any)
			if err := checkAnswer(ps, val); err != nil {
				return err
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for _, elem := range vv {
				if err := checkAnswer(items, elem); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func jsonTypes(raw any) []string {
	switch t := raw.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func matchesAnyType(types []string, v any) bool {
	for _, t := range types {
		if matchesType(t, v) {
			return true
		}
	}
	return false
}

func matchesType(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	default:
		return false
	}
}

// equalJSON compares two decoded JSON values. checkSchema and decodeAnswer
// both use UseNumber, so a schema-parsed number and a decoded answer number
// are both json.Number and compare equal on matching digits
// (TestCall_EnumOfNumbersComparesByJSONValue).
func equalJSON(a, b any) bool {
	an, aIsNum := a.(json.Number)
	bn, bIsNum := b.(json.Number)
	if aIsNum || bIsNum {
		return aIsNum && bIsNum && an == bn
	}
	return reflect.DeepEqual(a, b)
}
