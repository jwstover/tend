package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// Watcher answers one question cheaply: has any other connection committed
// to the database since I last asked? It is what lets the TUI notice writes
// made by other processes — an MCP-backed agent session, `tend add` from
// another shell, the workflow runner — without a daemon, a socket, or any
// cooperation from the writer.
//
// It rides on SQLite's PRAGMA data_version, which the SQLite docs describe
// for exactly this use ("interactive programs that display database content
// on-screen can use PRAGMA data_version to determine if they need to ...
// update the screen display"). The value is per-connection: it moves when
// a *different* connection commits, and never for the watching
// connection's own writes. So the watcher pins a single dedicated
// connection that is never written through, and every commit anywhere
// else — including through the owning Store's own pool, whose connections
// are "other" connections from the pinned one's point of view — moves it.
//
// One Watcher serves one goroutine at a time in practice, but Changed and
// Close are safe to call concurrently so the caller can tear it down
// without first proving the polling goroutine has stopped.
type Watcher struct {
	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	last   int64
	closed bool
}

// Watch opens a Watcher on the same database file this Store was opened
// on. It holds its own tiny *sql.DB (one connection, never recycled) so the
// pinned connection can't be swapped out from under the pragma read by
// the pool. The returned Watcher is primed: the first Changed reports only
// commits that land after Watch returns.
func (s *Store) Watch(ctx context.Context) (*Watcher, error) {
	db, err := sql.Open("sqlite", s.dsn)
	if err != nil {
		return nil, fmt.Errorf("opening watch db: %w", err)
	}
	// Exactly one connection, kept forever: data_version is meaningless
	// across connections, so the pool must never hand back a different one.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("pinning watch connection: %w", err)
	}
	w := &Watcher{db: db, conn: conn}
	if w.last, err = w.read(ctx); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

// Changed reports whether another connection has committed since the
// previous call (or since Watch, for the first call). A true result is
// consumed: the next call reports false until something else commits.
func (w *Watcher) Changed(ctx context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false, errors.New("watcher is closed")
	}
	v, err := w.read(ctx)
	if err != nil {
		return false, err
	}
	if v == w.last {
		return false, nil
	}
	w.last = v
	return true, nil
}

// read is the raw pragma read on the pinned connection; the caller holds
// the lock (or, in Watch, has not yet published the Watcher).
func (w *Watcher) read(ctx context.Context) (int64, error) {
	var v int64
	if err := w.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("reading data_version: %w", err)
	}
	return v, nil
}

// Close releases the pinned connection and its handle. Closing twice is a
// no-op, and a Changed racing with Close sees a closed-watcher error rather
// than a use of a released connection.
func (w *Watcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return errors.Join(w.conn.Close(), w.db.Close())
}
