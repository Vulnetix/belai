package harness

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed harnesses.json
var raw []byte

var (
	once sync.Once
	all  []Harness
)

func registry() []Harness {
	once.Do(func() {
		if err := json.Unmarshal(raw, &all); err != nil {
			panic("harness: harnesses.json: " + err.Error())
		}
	})
	return all
}
