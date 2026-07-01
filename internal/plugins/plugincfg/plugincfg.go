// Package plugincfg provides shared helpers for decoding plugin
// configuration values.
package plugincfg

import "fmt"

// ToFloat64 converts a configuration value to float64, accepting float64 or int.
func ToFloat64(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("must be a number, got %T", v)
	}
}
