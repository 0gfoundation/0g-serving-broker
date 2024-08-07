package model

type KeyPair struct {
	ZKPrivateKey []int64    `json:"zkPrivateKey"`
	ZKPublicKey  [2][]int64 `json:"zkPublicKey"`
}
