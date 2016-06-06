package main

import (
	"os"

	"github.com/elastic/beats/libbeat/beat"

	"github.com/michaelbaamonde/lambdabeat/beater"
)

func main() {
	err := beat.Run("lambdabeat", "", beater.New())
	if err != nil {
		os.Exit(1)
	}
}
