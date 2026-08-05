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

func TestParsePushSelection(t *testing.T) {
	selected, err := parsePushSelection("1, 3-5", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{1, 3, 4, 5} {
		if _, found := selected[index]; !found {
			t.Fatalf("selection is missing %d: %#v", index, selected)
		}
	}
	if _, found := selected[2]; found {
		t.Fatalf("selection unexpectedly contains 2: %#v", selected)
	}
	for _, value := range []string{"", "0", "2-1", "1-6", "word"} {
		if _, err := parsePushSelection(value, 5); err == nil {
			t.Fatalf("accepted invalid selection %q", value)
		}
	}
	all, err := parsePushSelection("all", 3)
	if err != nil || len(all) != 3 {
		t.Fatalf("all selection = %#v, %v", all, err)
	}
}
