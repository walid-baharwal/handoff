package handoff

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestInteractiveSelectionAndConfirmation(t *testing.T) {
	items := []handoffMetadata{
		{ID: "111111111111", Author: "Walid"},
		{ID: "222222222222", Author: "Saif"},
	}
	input := bufio.NewReader(strings.NewReader("2\nyes\n"))
	var output bytes.Buffer
	selected, err := selectHandoff(input, &output, items)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "222222222222" {
		t.Fatalf("selected ID = %q", selected.ID)
	}
	confirmed, err := confirmPull(input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("expected pull confirmation")
	}
}

func TestPrintHandoffMetadataSanitizesControlCharacters(t *testing.T) {
	item := handoffMetadata{
		ID:         testID,
		Author:     "Walid\x1b[31m",
		Project:    "handoff",
		Branch:     "main",
		Message:    "safe\nmessage",
		CreatedAt:  time.Now(),
		FileCount:  1,
		Files:      []string{"file\tname.go"},
		BaseCommit: strings.Repeat("a", 40),
	}
	var output bytes.Buffer
	printHandoffMetadata(&output, item)
	if strings.ContainsAny(output.String(), "\x1b\t") || !strings.Contains(output.String(), "safe message") {
		t.Fatalf("metadata output contains control characters: %q", output.String())
	}
}
