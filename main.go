package main

import (
	"os"

	"github.com/elastic/beats/libbeat/beat"

	"github.com/michaelbaamonde/lambdabeat/beater"
)

var Version = "0.0.1"

func main() {
	err := beat.Run("lambdabeat", Version, beater.New())
	if err != nil {
		os.Exit(1)
	}
}
