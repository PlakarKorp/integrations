package main

import "github.com/PlakarKorp/go-kloset-sdk"

func main() {
	err := sdk.RunExporter(NewCaldavExporter)
	if err != nil {
		panic(err)
	}
}
