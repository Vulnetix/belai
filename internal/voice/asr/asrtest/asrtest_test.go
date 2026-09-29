package asrtest

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestTinyModelIsDeterministicGgml(t *testing.T) {
	a, b := TinyModel("", EnglishVocab), TinyModel("", EnglishVocab)
	if !bytes.Equal(a, b) {
		t.Fatal("TinyModel is not deterministic")
	}
	if binary.LittleEndian.Uint32(a) != ggmlMagic {
		t.Fatal("TinyModel does not start with the ggml magic")
	}
	if bytes.Equal(a, TinyModel("decoder.ln.bias", EnglishVocab)) {
		t.Fatal("skipping a tensor changed nothing")
	}
	if bytes.Equal(a, TinyModel("", EnglishVocab+1)) {
		t.Fatal("a different vocabulary changed nothing")
	}
}
