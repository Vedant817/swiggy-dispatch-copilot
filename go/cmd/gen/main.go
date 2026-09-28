package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gen <world|traffic|burst> [flags] (wired in B3)")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "gen %s not implemented yet (phase B3)\n", flag.Arg(0))
	os.Exit(1)
}
