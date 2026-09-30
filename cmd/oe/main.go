package main

import (
	"context"
	"os"

	"github.com/open-e2ee/oe/internal/app"
)

var version = "0.0.0-development"

func main() {
	app.Version = version
	os.Exit(app.Run(context.Background(), os.Args[1:], app.Dependencies{}))
}
