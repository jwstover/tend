package mcpserver

import "context"

// Notifier posts a desktop notification for the notify step tool.
type Notifier func(ctx context.Context, title, body string) error

// notifier is set once per `tend mcp` process (SetNotifier) rather than
// carried on Server; nil means notifications are not available.
var notifier Notifier

// SetNotifier installs the function the notify tool sends through.
func SetNotifier(n Notifier) { notifier = n }
