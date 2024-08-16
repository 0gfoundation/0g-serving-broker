package constant

var (
	ServicePrefix = "/v1/proxy"

	RequestMetaData = map[string]struct{}{
		"Address":               {},
		"Fee":                   {},
		"Input-Count":           {},
		"Nonce":                 {},
		"Previous-Output-Count": {},
		"Service-Name":          {},
		"Signature":             {},
	}
)
