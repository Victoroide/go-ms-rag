package main

import (
	"os"

	"github.com/Victoroide/quarry-rag/internal/extractor"
)

func init() {
	if len(os.Args) == 2 && os.Args[1] == extractor.ChildArg && os.Getenv(extractor.ChildEnv) == "1" {
		os.Exit(extractor.RunChild(os.Stdin, os.Stdout))
	}
}
