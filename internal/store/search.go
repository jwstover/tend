package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

// maxSearchTerms is how many space-separated words SearchTasks honours;
// the query has a fixed slot per word, and extras are ignored.
const maxSearchTerms = 3

// SearchTasks finds tasks anywhere -- every project, every state, any
// depth -- whose title, body or tags contain every word of query
// (case-insensitive substring; the first maxSearchTerms words count).
// Title matches come first, then the most recently updated. An empty
// query matches nothing, and limit caps the rows returned.
func (s *Store) SearchTasks(ctx context.Context, query string, limit int) ([]task.Task, error) {
	words := strings.Fields(query)
	if len(words) == 0 || limit <= 0 {
		return nil, nil
	}
	// An empty slot matches everything (instr(x, '') is 1).
	var terms [maxSearchTerms]string
	copy(terms[:], words)
	rows, err := s.q.SearchTasks(ctx, gen.SearchTasksParams{
		Term1: terms[0], Term2: terms[1], Term3: terms[2], MaxRows: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("searching tasks for %q: %w", query, err)
	}
	return toDomainSlice(rows)
}
