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
}

type Writer struct {
	mode Mode
	out  io.Writer
}

func New(mode Mode, out io.Writer) *Writer {
	return &Writer{mode: mode, out: out}
}

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

func (w *Writer) Failure(command string, err error) error {
	if w.mode == Text {
		_, writeErr := fmt.Fprintf(w.out, "error: %v\n", err)
		return writeErr
	}
	return w.writeJSON(Event{Status: "error", Command: command, Error: err.Error()})
}

func (w *Writer) writeJSON(value Event) error {
	encoder := json.NewEncoder(w.out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
