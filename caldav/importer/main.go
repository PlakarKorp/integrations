package main

import "github.com/PlakarKorp/go-kloset-sdk"

func main() {
	err := sdk.RunImporter(NewCaldavImporter)
	if err != nil {
		panic(err)
	}
}
