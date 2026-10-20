package app

import (
	"io"

	"github.com/HaikeiLabs/kei-cli/internal/docs"
)

func runDocsCommand(args []string, stdout, stderr io.Writer, defaultBaseURL string) int {
	return docs.Run(args, stdout, stderr, defaultBaseURL)
}
