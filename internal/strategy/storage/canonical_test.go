package storage

import (
	"bytes"
	"testing"
)

func FuzzCanonicalCheckpointSerialization(f *testing.F) {
	checkpoint := checkpointFixture(f)
	encoded, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Fuzz(func(t *testing.T, data []byte) {
		checkpoint, err := DecodeCheckpoint(data)
		if err != nil {
			return
		}
		left, err := EncodeCheckpoint(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		right, err := EncodeCheckpoint(checkpoint)
		if err != nil || !bytes.Equal(left, right) {
			t.Fatal("serialization changed between runs")
		}
	})
}
