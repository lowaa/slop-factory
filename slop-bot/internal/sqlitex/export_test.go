package sqlitex

// OnWriterWait sets the hook a write transaction calls as it queues
// behind another.
func OnWriterWait(d *DB, f func()) { d.waiting = f }
