package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nurozen/context-marmot/internal/sdkgen"
)

// cmdSDK generates the TypeScript SDK and optionally writes it to a file.
func cmdSDK(args []string) int {
	fs := flag.NewFlagSet("sdk", flag.ContinueOnError)
	out := fs.String("out", "", "output file path (default: stdout)")
	baseURL := fs.String("base-url", "http://localhost:3000", "base URL for the generated SDK")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	content := sdkgen.Generate(*baseURL)

	if *out != "" {
		if err := os.WriteFile(*out, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "sdk: write %s: %v\n", *out, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "sdk: wrote %s\n", *out)
		return 0
	}

	fmt.Print(content)
	return 0
}
