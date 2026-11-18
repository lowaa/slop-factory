package seeddata

func init() {
	register(Registry{

		"ticket_sequences":       derivedSeeder("tickets"),
		"ticket_status_changes":  derivedSeeder("tickets"),
		"board_revisions":        derivedSeeder("tickets"),
		"board_changes":          derivedSeeder("tickets"),
		"attempts":               derivedSeeder("tickets"),
		"attempt_status_changes": derivedSeeder("tickets"),
	})
}
