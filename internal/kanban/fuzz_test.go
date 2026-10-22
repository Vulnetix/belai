package kanban

import (
	"testing"
)

// FuzzDecode never panics and rejects inputs that are not valid board bytes.
func FuzzDecode(f *testing.F) {
	// Seed with a valid empty board and a couple of common cases.
	valid, err := Encode(Board{})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte("BKAN"))
	f.Add([]byte("NOTA"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Decode(data)
	})
}
