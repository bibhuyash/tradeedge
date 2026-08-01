package canonicaljson

import (
	"bytes"
	"errors"
	"testing"
)

func TestObjectCanonicalizesAndRejectsAmbiguousAuthority(t *testing.T) {
	first, err := Object([]byte(`{"b":2,"a":{"z":1,"x":0}}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Object([]byte(`{"a":{"x":0,"z":1},"b":2}`), 1024)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("canonical mismatch: %s %s %v", first, second, err)
	}
	for _, raw := range []string{
		`{"a":1,"a":2}`, `{"a":1.0}`, `{"a":1e3}`, `{"a":01}`, `[]`, `{"a":1} trailing`,
	} {
		if _, err := Object([]byte(raw), 1024); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Object(%s) error = %v", raw, err)
		}
	}
}

func TestObjectBoundedDepthAndCollections(t *testing.T) {
	if _, err := ObjectBounded([]byte(`{"a":{"b":{"c":1}}}`), Limits{
		MaximumBytes: 1024, MaximumDepth: 2, MaximumCollection: 10,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("depth error = %v", err)
	}
	if _, err := ObjectBounded([]byte(`{"a":[1,2]}`), Limits{
		MaximumBytes: 1024, MaximumDepth: 4, MaximumCollection: 1,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("collection error = %v", err)
	}
}

func FuzzObject(f *testing.F) {
	f.Add([]byte(`{"a":1}`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			raw = raw[:4096]
		}
		canonical, err := ObjectBounded(raw, Limits{
			MaximumBytes: 4096, MaximumDepth: 16, MaximumCollection: 100,
		})
		if err == nil {
			repeated, repeatErr := ObjectBounded(canonical, Limits{
				MaximumBytes: 4096, MaximumDepth: 16, MaximumCollection: 100,
			})
			if repeatErr != nil || !bytes.Equal(canonical, repeated) {
				t.Fatalf("canonical output was not stable: %v", repeatErr)
			}
		}
	})
}
