package types_test

import (
	"reflect"
	"testing"

	"github.com/d3vi1/helianthus-ebusgo/types"
)

func TestDecodeFields(t *testing.T) {
	t.Parallel()

	fields := []types.Field{
		{Name: "first", Type: types.DATA1b{}},
		{Name: "second", Type: types.WORD{}},
	}
	payload := []byte{0x01, 0x34, 0x12}

	got, err := types.DecodeFields(payload, fields)
	if err != nil {
		t.Fatalf("DecodeFields error = %v", err)
	}

	want := map[string]types.Value{
		"first":  {Value: int8(1), Valid: true},
		"second": {Value: uint16(0x1234), Valid: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeFields = %#v; want %#v", got, want)
	}
}

func TestDecodeFields_ShortPayload(t *testing.T) {
	t.Parallel()

	fields := []types.Field{
		{Name: "first", Type: types.DATA1b{}},
		{Name: "second", Type: types.WORD{}},
	}
	_, err := types.DecodeFields([]byte{0x01}, fields)
	assertInvalidPayload(t, err)
}

func TestDecodeFields_FieldError(t *testing.T) {
	t.Parallel()

	fields := []types.Field{
		{Name: "bcd", Type: types.BCD{}},
	}
	_, err := types.DecodeFields([]byte{0x1A}, fields)
	assertInvalidPayload(t, err)
}

func TestTotalSize(t *testing.T) {
	t.Parallel()

	fields := []types.Field{
		{Name: "first", Type: types.DATA1b{}},
		{Name: "second", Type: types.WORD{}},
	}
	if got := types.TotalSize(fields); got != 3 {
		t.Fatalf("TotalSize = %d; want 3", got)
	}
}
