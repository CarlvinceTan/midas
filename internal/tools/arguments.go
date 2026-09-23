package tools

import (
	"encoding/json"
	"fmt"
	"math"
)

func stringArgument(arguments map[string]any, name string) (string, error) {
	value, ok := arguments[name]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	return text, nil
}

func optionalPositiveInt(arguments map[string]any, name string) (int, bool, error) {
	value, ok := arguments[name]
	if !ok || value == nil {
		return 0, false, nil
	}
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int64:
		number = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false, fmt.Errorf("argument %q must be a number", name)
		}
		number = parsed
	default:
		return 0, false, fmt.Errorf("argument %q must be a number", name)
	}
	// math.MaxInt rounds to exactly 2^63 as a float64, so values at or above it
	// must be rejected: converting them to int would overflow.
	if math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || math.Trunc(number) != number || number >= math.MaxInt {
		return 0, false, fmt.Errorf("argument %q must be a positive integer", name)
	}
	return int(number), true, nil
}
