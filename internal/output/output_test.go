package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONNeverSerializesAnUnrequestedEmptyDataObject(t *testing.T) {
	var buffer bytes.Buffer
	writer := New(JSON, &buffer, &bytes.Buffer{})
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
	writer := New(JSONStream, &buffer, &bytes.Buffer{})
	if err := writer.Progress("sandbox", "waiting", nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Success("sandbox", "ready", nil); err != nil {
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
	writer := New(JSON, &buffer, &bytes.Buffer{})
	if err := writer.Failure("login", Problem{Message: "credential rejected"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buffer.Bytes(), []byte(`"error":"credential rejected"`)) {
		t.Fatalf("unexpected failure output: %s", buffer.String())
	}
}

func TestFailureCarriesCodeAndNextCommand(t *testing.T) {
	var buffer bytes.Buffer
	writer := New(JSON, &buffer, &bytes.Buffer{})
	if err := writer.Failure("plan", Problem{Message: "login required", Code: "AUTHENTICATION_REQUIRED", Next: "oe login"}); err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(buffer.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Status != "error" || event.Code != "AUTHENTICATION_REQUIRED" || event.Next != "oe login" {
		t.Fatalf("unexpected failure event: %#v", event)
	}
}

func TestTextFailureKeepsStandardOutputClean(t *testing.T) {
	var stdout, stderr bytes.Buffer
	writer := New(Text, &stdout, &stderr)
	if err := writer.Failure("plan", Problem{Message: "login required", Next: "oe login"}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.String() != "error: login required\nnext: oe login\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
