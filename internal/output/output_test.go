package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestJSONNeverSerializesAnUnrequestedEmptyDataObject(t *testing.T) {
	var buffer bytes.Buffer
	writer := New(JSON, &buffer)
	if err := writer.Success("doctor", "healthy", nil); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if _, ok := event["data"]; ok {
		t.Fatal("empty data object was serialized")
	}
}

func TestJSONStreamEmitsProgressAndTerminalEvents(t *testing.T) {
	var buffer bytes.Buffer
	writer := New(JSONStream, &buffer)
	if err := writer.Progress("dev", "waiting", nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Success("dev", "ready", nil); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&buffer)
	for index, status := range []string{"progress", "ok"} {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		if event.Status != status {
			t.Fatalf("event %d status %q", index, event.Status)
		}
	}
}

func TestFailureDoesNotWrapOrInspectErrorDetails(t *testing.T) {
	var buffer bytes.Buffer
	writer := New(JSON, &buffer)
	if err := writer.Failure("login", errors.New("credential rejected")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buffer.Bytes(), []byte(`"error":"credential rejected"`)) {
		t.Fatalf("unexpected failure output: %s", buffer.String())
	}
}
