package login

// Identity is what an identity provider vouches for. Subject is the
// provider's stable ID; Login is display-only and may change. Email is only
// to be relied on when EmailVerified -- the provider then vouches that the
// person controls it; otherwise it is whatever they typed into a profile.
type Identity struct {
	Provider      string
	Subject       string
	Login         string
	DisplayName   string
	Email         string
	EmailVerified bool
}
