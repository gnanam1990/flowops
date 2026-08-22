// ascp-capacity-audit verifies immutable evidence from an externally driven,
// production-equivalent sustained peak run. It sends no traffic itself.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gnanam1990/flowops/internal/ascpcapacity"
)

const maximumEvidenceBytes = 256 << 20

func main() {
	profilePath := flag.String("profile", "", "strict JSON peak profile")
	evidencePath := flag.String("evidence", "", "strict JSON run evidence")
	flag.Parse()
	if *profilePath == "" || *evidencePath == "" {
		fmt.Fprintln(os.Stderr, "both -profile and -evidence are required")
		os.Exit(1)
	}
	var profile ascpcapacity.Profile
	if err := decodeFile(*profilePath, &profile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var evidence ascpcapacity.Evidence
	if err := decodeFile(*evidencePath, &evidence); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report := ascpcapacity.Analyze(profile, evidence)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
	if !report.Passed {
		os.Exit(2)
	}
}

func decodeFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	limited := io.LimitReader(file, maximumEvidenceBytes+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > maximumEvidenceBytes {
		return errors.New("capacity evidence file is empty or exceeds 256 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("capacity evidence contains trailing JSON")
	}
	return nil
}
