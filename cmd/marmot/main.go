package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot <command> [args]")
		fmt.Fprintln(os.Stderr, "commands: init, query, serve")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		fmt.Println("marmot init: not yet implemented")
	case "query":
		fmt.Println("marmot query: not yet implemented")
	case "serve":
		fmt.Println("marmot serve: not yet implemented")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
