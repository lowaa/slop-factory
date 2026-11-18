package login

// Seal seals an arbitrary payload with c's key, so a test can hand Decode
// input that passes the MAC but not what comes after it.
func (c *LoginStateCodec) Seal(payload []byte) string { return c.seal(payload) }
