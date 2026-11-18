package githubapp

func WebhookSecretForTest() *WebhookSecret {
	s, err := newWebhookSecret()
	if err != nil {
		panic(err)
	}
	return s
}
