package change

// landingTransitionsForTest exposes the table to the external test package.
func LandingTransitionsForTest() map[LandingState][]LandingState { return landingTransitions }
