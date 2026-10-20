package main

import (
	"encoding/json"
	"kvm-center/internal/legacyfixture"
	"os"
)

func main() {
	a, e := legacyfixture.CompositionAccounting()
	if e != nil {
		panic(e)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if e = enc.Encode(a); e != nil {
		panic(e)
	}
}
