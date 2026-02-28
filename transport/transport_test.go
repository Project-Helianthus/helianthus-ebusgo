package transport_test

import (
	"reflect"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func TestRawTransport_InterfaceShape(t *testing.T) {
	t.Parallel()

	iface := reflect.TypeOf((*transport.RawTransport)(nil)).Elem()
	if iface.NumMethod() != 3 {
		t.Fatalf("RawTransport has %d methods; want 3", iface.NumMethod())
	}

	byteType := reflect.TypeOf(byte(0))
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	readByte, ok := iface.MethodByName("ReadByte")
	if !ok {
		t.Fatal("ReadByte method missing")
	}
	if readByte.Type.NumIn() != 0 || readByte.Type.NumOut() != 2 {
		t.Fatalf("ReadByte signature = %v; want () (byte, error)", readByte.Type)
	}
	if readByte.Type.Out(0) != byteType || readByte.Type.Out(1) != errorType {
		t.Fatalf("ReadByte return types = (%v, %v); want (byte, error)", readByte.Type.Out(0), readByte.Type.Out(1))
	}

	write, ok := iface.MethodByName("Write")
	if !ok {
		t.Fatal("Write method missing")
	}
	if write.Type.NumIn() != 1 || write.Type.NumOut() != 2 {
		t.Fatalf("Write signature = %v; want ([]byte) (int, error)", write.Type)
	}
	inType := write.Type.In(0)
	if inType.Kind() != reflect.Slice || inType.Elem().Kind() != reflect.Uint8 {
		t.Fatalf("Write input type = %v; want []byte", inType)
	}
	if write.Type.Out(0).Kind() != reflect.Int || write.Type.Out(1) != errorType {
		t.Fatalf("Write return types = (%v, %v); want (int, error)", write.Type.Out(0), write.Type.Out(1))
	}

	closeMethod, ok := iface.MethodByName("Close")
	if !ok {
		t.Fatal("Close method missing")
	}
	if closeMethod.Type.NumIn() != 0 || closeMethod.Type.NumOut() != 1 {
		t.Fatalf("Close signature = %v; want () error", closeMethod.Type)
	}
	if closeMethod.Type.Out(0) != errorType {
		t.Fatalf("Close return type = %v; want error", closeMethod.Type.Out(0))
	}
}
