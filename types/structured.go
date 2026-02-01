package types

import (
	"fmt"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

// Field describes a named data type within a structured payload.
type Field struct {
	Name string
	Type DataType
}

// DecodeFields decodes sequential fields from the payload.
func DecodeFields(payload []byte, fields []Field) (map[string]Value, error) {
	values := make(map[string]Value, len(fields))
	offset := 0
	for _, field := range fields {
		if field.Type == nil {
			return nil, fmt.Errorf("field %q missing type: %w", field.Name, ebuserrors.ErrInvalidPayload)
		}
		size := field.Type.Size()
		if size <= 0 {
			return nil, fmt.Errorf("field %q invalid size: %w", field.Name, ebuserrors.ErrInvalidPayload)
		}
		if offset+size > len(payload) {
			return nil, fmt.Errorf("field %q short payload: %w", field.Name, ebuserrors.ErrInvalidPayload)
		}
		value, err := field.Type.Decode(payload[offset : offset+size])
		if err != nil {
			return nil, fmt.Errorf("field %q decode: %w", field.Name, err)
		}
		values[field.Name] = value
		offset += size
	}
	return values, nil
}

// TotalSize returns the total byte size required for the fields.
func TotalSize(fields []Field) int {
	total := 0
	for _, field := range fields {
		if field.Type == nil {
			continue
		}
		size := field.Type.Size()
		if size > 0 {
			total += size
		}
	}
	return total
}
