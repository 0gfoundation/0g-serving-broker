package model

type KeyPair struct {
	ZKPrivateKey []string  `json:"zkPrivateKey"`
	ZKPublicKey  [2]string `json:"zkPublicKey"`
}
