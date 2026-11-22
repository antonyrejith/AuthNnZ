package bus

// EmbeddedListens reports whether the embedded server owned by b has a TCP
// listener (false when b owns none).
func EmbeddedListens(b *Bus) bool { return b.embedded != nil && b.embedded.ns.Addr() != nil }

// OwnsEmbedded reports whether b owns an embedded server.
func OwnsEmbedded(b *Bus) bool { return b.embedded != nil }
