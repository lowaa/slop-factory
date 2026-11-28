// Package agytest cleans up after tests that run the real agy: what agy
// keeps of their conversations, so the user's own conversation list, `agy
// --continue` and prompt history aren't left with the tests'.
package agytest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ForgetConversation removes what agy keeps of a conversation under home.
func ForgetConversation(home, id string) error {
	// A blank id would name the whole brain directory: every conversation
	// of the user's.
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return fmt.Errorf("agytest: %q isn't a conversation id", id)
	}
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	errs := []error{os.RemoveAll(filepath.Join(root, "brain", id))}
	for _, f := range []string{
		filepath.Join("conversations", id+".db"), filepath.Join("conversations", id+".db-wal"),
		filepath.Join("conversations", id+".db-shm"), filepath.Join("annotations", id+".pbtxt"),
	} {
		if err := os.Remove(filepath.Join(root, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DropHistory removes the prompts typed in workspace from agy's prompt
// history under home, keeping every other line as it was. A prompt agy
// records as it dies, after this ran, stays.
func DropHistory(home, workspace string) error {
	path := filepath.Join(home, ".gemini", "antigravity-cli", "history.jsonl")
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- agy's own history
	if err != nil {
		return err
	}
	var kept bytes.Buffer
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		var h struct {
			Workspace string `json:"workspace"`
		}
		if json.Unmarshal(bytes.TrimSpace(line), &h) == nil && h.Workspace == workspace {
			continue
		}
		kept.Write(line)
	}
	if kept.Len() == len(data) {
		return nil
	}
	tmp := path + ".slop-bot-tmp"
	if err := os.WriteFile(tmp, kept.Bytes(), fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
