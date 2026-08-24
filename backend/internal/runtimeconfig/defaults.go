package runtimeconfig

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

//go:embed defaults-v1.json
var rawDefaults []byte

type defaultValue struct {
	Runtime string `json:"runtime"`
}

var values = loadDefaults()

func loadDefaults() map[string]defaultValue {
	var contract struct {
		Version  int                     `json:"version"`
		Defaults map[string]defaultValue `json:"defaults"`
	}
	if err := json.Unmarshal(rawDefaults, &contract); err != nil || contract.Version != 1 {
		panic(fmt.Sprintf("invalid runtime defaults contract: version=%d error=%v", contract.Version, err))
	}
	return contract.Defaults
}

func String(name string) string {
	value, ok := Lookup(name)
	if !ok {
		panic("runtime default missing from contract: " + name)
	}
	return value
}

func Lookup(name string) (string, bool) {
	value, ok := values[name]
	return value.Runtime, ok
}

func Int(name string) int {
	value, err := strconv.Atoi(String(name))
	if err != nil {
		panic("runtime default is not an integer: " + name)
	}
	return value
}

func Duration(name string) time.Duration {
	value, err := time.ParseDuration(String(name))
	if err != nil {
		panic("runtime default is not a duration: " + name)
	}
	return value
}
