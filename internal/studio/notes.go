package studio

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Note is one piece of live producer direction for a production.
type Note struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// AppendNote durably records a producer note in the workspace.
func AppendNote(workspace, text string) error {
	f, err := os.OpenFile(filepath.Join(workspace, "notes.jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(Note{Time: time.Now(), Text: text})
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadNotes returns every note in arrival order.
func ReadNotes(workspace string) ([]Note, error) {
	f, err := os.Open(filepath.Join(workspace, "notes.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Note
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var n Note
		if json.Unmarshal(sc.Bytes(), &n) == nil {
			out = append(out, n)
		}
	}
	return out, sc.Err()
}
