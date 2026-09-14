package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/vulnetix/signet/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	fmt.Println("signet", version.Version)
}
