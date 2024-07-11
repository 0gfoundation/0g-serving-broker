package constant

var (
	ServicePrefix = "/v1/proxy"

	RequestMetaData = map[string]struct{}{
		"Address":                     {},
		"Created-At":                  {},
		"Service-Name":                {},
		"Nonce":                       {},
		"Previous-Output-Token-Count": {},
		"Signature":                   {},
		"Token-Count":                 {},
	}
)
