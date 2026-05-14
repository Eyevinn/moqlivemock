package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	command := "experiments"
	if len(args) > 0 {
		switch args[0] {
		case "experiments", "testgen", "roundtrip":
			command = args[0]
			args = args[1:]
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	var err error
	switch command {
	case "experiments":
		err = runExperimentsCommand(args)
	case "testgen":
		err = runTestFileGeneratorCommand(args)
	case "roundtrip":
		err = runRoundtripCommand(args)
	default:
		err = fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "locmaf %s failed: %v\n", command, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "usage: locmaf [experiments|testgen|roundtrip] [flags]\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "commands:\n")
	fmt.Fprintf(os.Stderr, "  experiments  run LOCMAF experiments (default)\n")
	fmt.Fprintf(os.Stderr, "  testgen      generate LOCMAF test files\n")
	fmt.Fprintf(os.Stderr, "  roundtrip    encode an fMP4 through LOCMAF and verify fidelity\n")
}
