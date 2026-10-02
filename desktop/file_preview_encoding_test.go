package main

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// A GB18030 file larger than the preview is cut at 2 MiB, which here lands inside
// a two-byte character; the preview must still read as GB18030 text.
func TestReadFileGB18030TruncatedMidCharacter(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	line, _ := simplifiedchinese.GB18030.NewEncoder().String(strings.Repeat("中", 50) + "\n")
	if filePreviewLimit%len(line)%2 == 0 {
		t.Fatal("fixture does not cut a character at the preview limit")
	}
	if err := os.WriteFile("big.gbk", []byte(strings.Repeat(line, filePreviewLimit/len(line)+10)), 0o644); err != nil {
		t.Fatal(err)
	}
	p := (&App{}).ReadFile("big.gbk")
	if p.Binary || !p.Truncated {
		t.Fatalf("Binary = %v, Truncated = %v; want a truncated text preview", p.Binary, p.Truncated)
	}
	if !strings.HasPrefix(p.Body, strings.Repeat("中", 50)+"\n") || strings.ContainsRune(p.Body, '�') {
		t.Fatalf("preview did not decode as GB18030: %q...", p.Body[:min(len(p.Body), 60)])
	}
}

// With no newline anywhere in the preview, the cut character is dropped by
// itself rather than read as proof the text is not GB18030.
func TestReadFileGB18030TruncatedWithoutNewline(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	gb, _ := simplifiedchinese.GB18030.NewEncoder().String("x" + strings.Repeat("中", filePreviewLimit/2+10))
	if err := os.WriteFile("line.gbk", []byte(gb), 0o644); err != nil {
		t.Fatal(err)
	}
	p := (&App{}).ReadFile("line.gbk")
	if p.Binary || !p.Truncated {
		t.Fatalf("Binary = %v, Truncated = %v; want a truncated text preview", p.Binary, p.Truncated)
	}
	if !strings.HasPrefix(p.Body, "x中中") || strings.ContainsRune(p.Body, '�') {
		t.Fatalf("preview did not decode as GB18030: %q...", p.Body[:min(len(p.Body), 60)])
	}
	if p.NextOffset != filePreviewLimit-1 {
		t.Fatalf("NextOffset = %d, want %d", p.NextOffset, filePreviewLimit-1)
	}
}
