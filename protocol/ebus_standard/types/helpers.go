package types

// toInt64 normalises common Go integer types to int64. Floats and strings
// are rejected so that encode rules remain strict.
func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int8:
		return int64(t), true
	case int16:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case uint:
		return int64(t), true
	case uint8:
		return int64(t), true
	case uint16:
		return int64(t), true
	case uint32:
		return int64(t), true
	case uint64:
		if t > (1<<63 - 1) {
			return 0, false
		}
		return int64(t), true
	default:
		return 0, false
	}
}

// toFloat64 normalises numeric inputs to float64 for DATA1c-style codecs.
func toFloat64(v any) (float64, bool) {
	switch t := v.(type) {
	case float32:
		return float64(t), true
	case float64:
		return t, true
	default:
		if i, ok := toInt64(v); ok {
			return float64(i), true
		}
		return 0, false
	}
}
