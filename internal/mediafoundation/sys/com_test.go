//go:build windows && (amd64 || arm64)

package sys

import (
	"reflect"
	"testing"
)

// countMethods counts the vtable slots of a vtable struct, following the
// embedded parent interfaces.
func countMethods(t reflect.Type) int {
	n := 0
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			n += countMethods(f.Type)
		} else {
			n++
		}
	}
	return n
}

func TestVtableSizes(t *testing.T) {
	for _, c := range []struct {
		v    any
		want int
	}{
		{iUnknownVtbl{}, 3},
		{iMFAttributesVtbl{}, 3 + 30},
		{iMFMediaTypeVtbl{}, 33 + 5},
		{iMFActivateVtbl{}, 33 + 3},
		{iMFSampleVtbl{}, 33 + 14},
		{iMFMediaBufferVtbl{}, 3 + 5},
		{iMF2DBufferVtbl{}, 3 + 7},
		{iMF2DBuffer2Vtbl{}, 10 + 2},
		{iMFMediaSourceVtbl{}, 3 + 4 + 6},
		{iMFSourceReaderVtbl{}, 3 + 10},
	} {
		typ := reflect.TypeOf(c.v)
		if got := countMethods(typ); got != c.want {
			t.Errorf("%s: %d methods, want %d", typ.Name(), got, c.want)
		}
	}
}
