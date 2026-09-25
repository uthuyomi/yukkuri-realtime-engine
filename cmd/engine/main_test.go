package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestMalformedEnvironmentDoesNotLogCredentialContents(t *testing.T) {
	t.Chdir(t.TempDir())
	const secret = "private-credential-value"
	if err := os.WriteFile(".env", []byte("OPENAI_API_KEY='"+secret), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	loadEnvironment()
	if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), ".env not loaded") {
		t.Fatal("expected a sanitized environment failure diagnostic")
	}
}
