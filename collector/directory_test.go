package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeQueryFile(t *testing.T, dir, name, prefix string, count int) {
	t.Helper()
	qs := make([]query, count)
	for i := range qs {
		qs[i] = fixture()
		qs[i].Shape = "VM.Standard2.1"
		qs[i].Name = fmt.Sprintf("%s-%d", prefix, i)
	}
	data, err := json.Marshal(qs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestDirectoryLoadsMoreThanOneThousandQueries(t *testing.T) {
	dir := t.TempDir()
	writeQueryFile(t, dir, "iad.json", "iad", 600)
	writeQueryFile(t, dir, "lhr.json", "lhr", 600)
	// Simulate the symlinks used by Kubernetes projected ConfigMap volumes.
	volume := t.TempDir()
	for _, name := range []string{"iad.json", "lhr.json"} {
		if err := os.Symlink(filepath.Join(dir, name), filepath.Join(volume, name)); err != nil {
			t.Fatal(err)
		}
	}
	qs, err := loadQueries(volume)
	if err != nil || len(qs) != 1200 || qs[0].Name != "iad-0" || qs[600].Name != "lhr-0" {
		t.Fatalf("queries=%d err=%v", len(qs), err)
	}
}
func TestDirectoryRejectsDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	writeQueryFile(t, dir, "a.json", "same", 1)
	writeQueryFile(t, dir, "b.json", "same", 1)
	if _, err := loadQueries(dir); err == nil || !strings.Contains(err.Error(), "duplicate query name") {
		t.Fatal(err)
	}
}
func TestDirectoryRejectsEmptyAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadQueries(dir); err == nil {
		t.Fatal("accepted empty directory")
	}
	writeQueryFile(t, dir, "valid.json", "valid", 1)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`[{"typo":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadQueries(dir); err == nil {
		t.Fatal("accepted malformed regional file")
	}
}
