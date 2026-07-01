package admin

import (
	"reflect"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// isEnvRef reports whether s is a bare environment-variable reference of the
// form "${VAR}" (start "${"  end "}"). These are template placeholders that
// survive to the serialized config intentionally; the value they reference is
// resolved from the process environment at runtime and never stored.
func isEnvRef(s string) bool {
	if len(s) <= 3 || s[0] != '$' || s[1] != '{' || s[len(s)-1] != '}' {
		return false
	}
	name := s[2 : len(s)-1]
	if len(name) == 0 {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z'):
			// valid at any position
		case i > 0 && '0' <= r && r <= '9':
			// digits valid after first char
		default:
			return false
		}
	}
	return true
}

// redactedPlaceholder is substituted for any literal secret value when a Config
// is serialized to an admin API response.
const redactedPlaceholder = "[REDACTED]"

// scrubStringValue returns redactedPlaceholder for any literal string value.
// ${VAR} env references are preserved because they contain no secret material.
func scrubStringValue(v string) string {
	if isEnvRef(v) {
		return v
	}
	return redactedPlaceholder
}

// scrubStringMap returns a new map with every non-env-ref string value replaced
// by "[REDACTED]". The original map is not modified.
func scrubStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = scrubStringValue(v)
	}
	return out
}

// scrubAnyValue returns a scrubbed copy of v. Strings are redacted unless they
// are env-var references. map[string]any and slice values are recursively
// scrubbed into new copies — originals are never aliased or mutated.
// Non-string, non-map, non-slice values (bool, number, etc.) are copied as-is.
func scrubAnyValue(v any) any {
	switch val := v.(type) {
	case string:
		return scrubStringValue(val)
	case map[string]any:
		return scrubAnyMap(val)
	case []string:
		out := make([]string, len(val))
		for i, s := range val {
			out[i] = scrubStringValue(s)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = scrubAnyValue(elem)
		}
		return out
	case map[string]string:
		return scrubStringMap(val)
	case []map[string]any:
		out := make([]map[string]any, len(val))
		for i, elem := range val {
			out[i] = scrubAnyMap(elem)
		}
		return out
	case []map[string]string:
		out := make([]map[string]string, len(val))
		for i, elem := range val {
			out[i] = scrubStringMap(elem)
		}
		return out
	default:
		return scrubReflectValue(v)
	}
}

// scrubReflectValue recursively scrubs typed maps and slices that the concrete
// fast-path cases in scrubAnyValue do not enumerate (e.g. map[string][]string or
// [][]string). It walks the value via reflection and returns newly-created
// containers so nothing aliases the live config. Values that are neither maps
// nor slices are returned unchanged.
func scrubReflectValue(v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), reflectScrubbedElem(rv.Type().Elem(), iter.Value()))
		}
		return out.Interface()
	case reflect.Slice:
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(reflectScrubbedElem(rv.Type().Elem(), rv.Index(i)))
		}
		return out.Interface()
	default:
		return v
	}
}

// reflectScrubbedElem scrubs a single map/slice element and returns a
// reflect.Value assignable to elemType, falling back to the element type's zero
// value when the scrubbed result is an untyped nil.
func reflectScrubbedElem(elemType reflect.Type, elem reflect.Value) reflect.Value {
	scrubbed := scrubAnyValue(elem.Interface())
	sv := reflect.ValueOf(scrubbed)
	if !sv.IsValid() {
		return reflect.Zero(elemType)
	}
	return sv
}

// scrubAnyMap returns a new map with every non-env-ref string value replaced
// by "[REDACTED]". Nested map[string]any and slice values are recursively
// scrubbed into new copies — the original maps and slices are never aliased or
// mutated. Non-string, non-map, non-slice values (bool, number, etc.) are
// copied as-is. The original map is not modified.
func scrubAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = scrubAnyValue(v)
	}
	return out
}

// scrubConfigSecrets returns a shallow copy of cfg with secret-bearing map
// values replaced by "[REDACTED]". The live in-memory Config is never mutated.
//
// Fields scrubbed:
//   - Observability.Tracing.Headers (map[string]string)
//   - each Observability.Exporters[i].Config (map[string]any)
//   - each Plugins[i].Config (map[string]interface{})
//
// Values that look like "${ENV_VAR}" references are preserved because they
// contain no secret material; the actual secret is resolved from the process
// environment at runtime.
func scrubConfigSecrets(cfg aigateway.Config) aigateway.Config {
	cfg.Observability.Tracing.Headers = scrubStringMap(cfg.Observability.Tracing.Headers)

	if cfg.Observability.Exporters != nil {
		exporters := make([]aigateway.ExporterConfig, len(cfg.Observability.Exporters))
		for i, exp := range cfg.Observability.Exporters {
			exp.Config = scrubAnyMap(exp.Config)
			exporters[i] = exp
		}
		cfg.Observability.Exporters = exporters
	}

	if cfg.Plugins != nil {
		plugins := make([]aigateway.PluginConfig, len(cfg.Plugins))
		for i, p := range cfg.Plugins {
			p.Config = scrubAnyMap(p.Config)
			plugins[i] = p
		}
		cfg.Plugins = plugins
	}

	return cfg
}
