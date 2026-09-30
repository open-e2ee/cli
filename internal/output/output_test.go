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
	if err := writer.Failure("auth login", Problem{Message: "credential rejected"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buffer.Bytes(), []byte(`"error":"credential rejected"`)) {
		t.Fatalf("unexpected failure output: %s", buffer.String())
	}
}

func TestFailureCarriesCodeAndNextCommand(t *testing.T) {
	var buffer bytes.Buffer
	writer := New(JSON, &buffer, &bytes.Buffer{})
	if err := writer.Failure("plan", Problem{Message: "login required", Code: "AUTHENTICATION_REQUIRED", Next: "oe auth login"}); err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(buffer.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Status != "error" || event.Code != "AUTHENTICATION_REQUIRED" || event.Next != "oe auth login" {
		t.Fatalf("unexpected failure event: %#v", event)
	}
}

func TestTextFailureKeepsStandardOutputClean(t *testing.T) {
	var stdout, stderr bytes.Buffer
	writer := New(Text, &stdout, &stderr)
	if err := writer.Failure("plan", Problem{Message: "login required", Next: "oe auth login"}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.String() != "error: login required\nnext: oe auth login\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestPendingPrecedesTheResultInBothJSONModes(t *testing.T) {
	for _, mode := range []Mode{JSON, JSONStream} {
		var buffer bytes.Buffer
		writer := New(mode, &buffer, &bytes.Buffer{})
		action := Action{Kind: "browser", URL: "https://login.example/device", Reason: "login"}
		if err := writer.Pending("auth login", "A person must approve this device.", action, map[string]any{"userCode": "ABCD"}); err != nil {
			t.Fatal(err)
		}
		if err := writer.SuccessNext("auth login", "Logged in.", "oe auth login --accept-terms", nil); err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(&buffer)
		var pending, result Event
		if err := decoder.Decode(&pending); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if pending.Status != "pending" || pending.Action != action || result.Status != "ok" || result.Next != "oe auth login --accept-terms" || result.Action != (Action{}) {
			t.Fatalf("%s wrote %#v then %#v", mode, pending, result)
		}
	}
}

func TestTextSuccessNamesTheNextCommand(t *testing.T) {
	var stdout bytes.Buffer
	writer := New(Text, &stdout, &bytes.Buffer{})
	if err := writer.SuccessNext("auth login", "Logged in.", "oe auth login --accept-terms", nil); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Logged in.\nnext: oe auth login --accept-terms\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
