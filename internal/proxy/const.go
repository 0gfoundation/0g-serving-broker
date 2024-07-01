package proxy

var (
	servicePrefix = "/v1/proxy"

	requestMetaData = map[string]struct{}{
		"Address":                     {},
		"Created-At":                  {},
		"Service-Name":                {},
		"Nonce":                       {},
		"Previous-Output-Token-Count": {},
		"Signature":                   {},
		"Token-Count":                 {},
	}
)
