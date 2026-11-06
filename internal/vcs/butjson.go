package vcs

import (
	"encoding/json"
	"fmt"
	"strings"
)

type butDiff struct {
	Changes []struct {
		Path   string `json:"path"`
		Status string `json:"status"`
		Diff   struct {
			Type  string `json:"type"`
			Hunks []struct {
				Diff string `json:"diff"`
			} `json:"hunks"`
		} `json:"diff"`
	} `json:"changes"`
}

// ParseButDiff maps `but diff <id> --json` output onto a Diff.
func ParseButDiff(data []byte) (Diff, error) {
	var in butDiff
	if err := json.Unmarshal(data, &in); err != nil {
		return Diff{}, fmt.Errorf("decoding but diff: %w", err)
	}
	var d Diff
	for _, c := range in.Changes {
		f := File{OldPath: c.Path, NewPath: c.Path, Status: changeStatus(c.Status)}
		switch f.Status {
		case StatusAdded:
			f.OldPath = ""
		case StatusDeleted:
			f.NewPath = ""
		}
		if c.Diff.Type != "patch" {
			f.Binary = true
		}
		for _, h := range c.Diff.Hunks {
			parsed, err := ParseUnified("diff --git a/x b/x\n" + strings.TrimRight(h.Diff, "\n") + "\n")
			if err != nil {
				return Diff{}, err
			}
			for _, pf := range parsed.Files {
				f.Hunks = append(f.Hunks, pf.Hunks...)
			}
		}
		f.count()
		d.Files = append(d.Files, f)
	}
	return d, nil
}

func changeStatus(s string) Status {
	switch strings.ToLower(s) {
	case "added", "new":
		return StatusAdded
	case "deleted", "removed":
		return StatusDeleted
	case "renamed":
		return StatusRenamed
	}
	return StatusModified
}
