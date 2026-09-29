package main

import "testing"

func TestRunWTFRejectsArguments(t *testing.T) {
	err := runWTF(Config{WTFArgs: []string{"status"}})
	if err == nil || err.Error() != "wtf: unsupported arguments: status" {
		t.Fatalf("runWTF(status) error = %v", err)
	}
}
