package types

// Value represents a decoded value with validity information.
type Value struct {
	Value any
	Valid bool
}

// DataType defines encoding and decoding for a single eBUS data type.
type DataType interface {
	Decode([]byte) (Value, error)
	Encode(any) ([]byte, error)
	Size() int
	ReplacementValue() []byte
}
