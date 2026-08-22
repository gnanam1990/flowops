package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gnanam1990/flowops/internal/acceptance"
)

func main() {
	path := flag.String("manifest", "docs/acceptance/ascp-v3.4.json", "acceptance manifest path")
	jsonOutput := flag.Bool("json", false, "print a JSON summary")
	flag.Parse()
	manifest, err := acceptance.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	summary := manifest.Summary()
	if *jsonOutput {
		encoded, err := json.Marshal(summary)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(encoded))
		return
	}
	fmt.Println(summary.String())
}
