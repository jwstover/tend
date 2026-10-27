package mcpserver

import "context"

// Notification is what the notify tool hands its Notifier. Activate is the
// bundle id to bring forward on a click when URL is empty; "" lets the
// notifier choose its default.
type Notification struct {
	Title, Body, URL, Activate string
}

// Notifier posts a desktop notification for the notify step tool.
type Notifier func(ctx context.Context, n Notification) error

// notifier is set once per `tend mcp` process (SetNotifier) rather than
// carried on Server; nil means notifications are not available.
var notifier Notifier

// SetNotifier installs the function the notify tool sends through.
func SetNotifier(n Notifier) { notifier = n }
