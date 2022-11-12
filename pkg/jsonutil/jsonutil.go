package jsonutil

import (
	"encoding/json"
)

func Dump(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func DumpIndent(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
