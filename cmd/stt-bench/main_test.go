package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBenchmarkOutputContract(t *testing.T) {
	b, e := json.Marshal(record{})
	if e != nil {
		t.Fatal(e)
	}
	var got map[string]any
	if json.Unmarshal(b, &got) != nil {
		t.Fatal("JSON")
	}
	for _, field := range []string{"mode", "runtime", "fixture", "audio_seconds", "wall_ms", "rtf", "eot_to_final_ms", "first_partial_ms", "finalization_ms", "initialization_ms", "measurement"} {
		if _, ok := got[field]; !ok {
			t.Fatal(field)
		}
	}
	if got["first_partial_ms"] != nil || got["finalization_ms"] != nil {
		t.Fatal("unmeasured values must be null")
	}
}
func TestBenchmarkRejectsUnsupportedAndMalformedInputs(t *testing.T) {
	for _, args := range [][]string{{"-mode", "streaming"}, {"-repeat", "0"}, {"-durations", "0"}, {"-mode", "legacy", "-device", "cuda"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal(args)
		}
	}
	path := filepath.Join(t.TempDir(), "bad.wav")
	os.WriteFile(path, []byte("not wave"), 0600)
	if _, e := readWAV(path); e == nil {
		t.Fatal("invalid WAV accepted")
	}
}
