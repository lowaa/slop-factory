package state

// SetBeforeWait and SetBeforeRead install Follow's test hooks. Call them
// before any Follow.
func SetBeforeWait(e *Events, f func()) { e.beforeWait = f }

func SetBeforeRead(e *Events, f func()) { e.beforeRead = f }
