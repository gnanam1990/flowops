package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRequirementsPrintsTheCompleteExternalInventory(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"requirements"}, &output, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"AC-1", "AC-2", "AC-5", "AC-12", "AC-19", "AC-24", "AC-31", "AC-33", "AC-37", "AC-46", "AC-47", "AC-53", "AC-68", "AC-84"} {
		if !strings.Contains(output.String(), `"`+id+`"`) {
			t.Fatalf("requirements output lacks %s", id)
		}
	}
}

func TestRunRejectsUnknownAndIncompleteCommands(t *testing.T) {
	for _, args := range [][]string{{}, {"digest"}, {"template"}, {"verify", "bundle"}, {"unknown"}} {
		if err := run(args, &bytes.Buffer{}, time.Now()); err == nil {
			t.Fatalf("command %v was accepted", args)
		}
	}
}
