package constant

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/chat/completion": {},
	}

	RequestMetaData = map[string]struct{}{
		"Address":             {},
		"Fee":                 {},
		"Input-Fee":           {},
		"Nonce":               {},
		"Previous-Output-Fee": {},
		"Service-Name":        {},
		"Signature":           {},
	}
)
