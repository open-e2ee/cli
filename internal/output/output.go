package output

import (
	"encoding/json"
	"fmt"
	"io"
)

type Mode string

const (
	Text       Mode = "text"
	JSON       Mode = "json"
	JSONStream Mode = "json-stream"
)

type Event struct {
	Status  string         `json:"status"`
	Command string         `json:"command"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
	Code    string         `json:"code,omitempty"`
	Next    string         `json:"next,omitempty"`
}

// Problem is a failed command. Code is stable for a caller to switch on, and
// Next names the command that moves the caller forward.
type Problem struct {
	Message string
	Code    string
	Next    string
	Data    map[string]any
}

type Writer struct {
	mode   Mode
	out    io.Writer
	errOut io.Writer
}

// New writes results to out. A JSON mode also writes failures to out, so each
// run emits parseable documents on one stream. Text mode writes failures to
// errOut.
func New(mode Mode, out, errOut io.Writer) *Writer {
	return &Writer{mode: mode, out: out, errOut: errOut}
}

func (w *Writer) Mode() Mode { return w.mode }

func (w *Writer) Progress(command, message string, data map[string]any) error {
	if w.mode == Text {
		_, err := fmt.Fprintln(w.out, message)
		return err
	}
	if w.mode != JSONStream {
		return nil
	}
	return w.writeJSON(Event{Status: "progress", Command: command, Message: message, Data: data})
}

func (w *Writer) Success(command, message string, data map[string]any) error {
	if w.mode == Text {
		_, err := fmt.Fprintln(w.out, message)
		return err
	}
	return w.writeJSON(Event{Status: "ok", Command: command, Message: message, Data: data})
}

func (w *Writer) Failure(command string, problem Problem) error {
	if w.mode == Text {
		if _, err := fmt.Fprintf(w.errOut, "error: %s\n", problem.Message); err != nil {
			return err
		}
		if problem.Next != "" {
			_, err := fmt.Fprintf(w.errOut, "next: %s\n", problem.Next)
			return err
		}
		return nil
	}
	return w.writeJSON(Event{
		Status: "error", Command: command, Error: problem.Message,
		Code: problem.Code, Next: problem.Next, Data: problem.Data,
	})
}

func (w *Writer) writeJSON(value Event) error {
	encoder := json.NewEncoder(w.out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
