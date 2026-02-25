package constant

import (
	"github.com/ethereum/go-ethereum/crypto"
)

// ModelConfig defines the configuration for each fine-tuning model
type ModelConfig struct {
	TrainingScript   string // Path to the training script in Docker container
	PriceCoefficient int64  // Training cost multiplier (e.g., 8 for Qwen3-32B, 1 for others)
	StorageFee       int64  // Fixed storage fee in token units (e.g., 4 = 0.04 tokens for ~300MB LoRA, 1 = 0.01 tokens for ~100MB)
}

var (
	MOCK_MODEL_ROOT_HASH = "0xcb42b5ca9e998c82dd239ef2d20d22a4ae16b3dc0ce0a855c93b52c7c2bab6dc"

	// TODO: For MVP, this is hardcoded to true. In the future, this should can be configurable.
	IS_TURBO = true

	// SCRIPT_MAP maps model hash to its configuration including training script, price coefficient, and storage fee
	// Fee calculation: (tokenSize × pricePerToken × trainEpochs × PriceCoefficient) + StorageFee
	SCRIPT_MAP = map[string]ModelConfig{
		// "0x8645816c17a8a70ebf32bcc7e621c659e8d0150b1a6bfca27f48f83010c6d12e": {
		// 	TrainingScript:   "/app/finetune-img.py",
		// 	PriceCoefficient: 1,
		// 	StorageFee:       1, // ~100 MB LoRA
		// },
		// "0x7f2244b25cd2219dfd9d14c052982ecce409356e0f08e839b79796e270d110a7": {
		// 	TrainingScript:   "/app/finetune.py",
		// 	PriceCoefficient: 1,
		// 	StorageFee:       1, // ~100 MB LoRA
		// },
		// "0x2084fdd904c9a3317dde98147d4e7778a40e076b5b0eb469f7a8f27ae5b13e7f": {
		// 	TrainingScript:   "/app/finetune.py",
		// 	PriceCoefficient: 1,
		// 	StorageFee:       1, // ~100 MB LoRA
		// },
		// "0xcb42b5ca9e998c82dd239ef2d20d22a4ae16b3dc0ce0a855c93b52c7c2bab6dc": {
		// 	TrainingScript:   "/app/finetune.py",
		// 	PriceCoefficient: 1,
		// 	StorageFee:       1, // ~100 MB LoRA
		// },
		// "0x02ed6d3889bebad9e2cd4008066478654c0886b12ad25ea7cf7d31df3441182e": {
		// 	TrainingScript:   "/app/CocktailSGD/finetune-cocktail.py",
		// 	PriceCoefficient: 1,
		// 	StorageFee:       1, // ~100 MB LoRA
		// },
		// Qwen2.5-0.5B-Instruct (turbo)
		"0xb4f76a886b8655c92bb021922d60b5e4d9271a5c9da98b6cb10937a06c2c75a7": {
			TrainingScript:   "/app/train_lora.py",
			PriceCoefficient: 1,
			StorageFee:       10000000000000000, // ~100 MB LoRA: 0.01 tokens
		},
		// Qwen3-32B (turbo)
		"0x2e6f9620c35bdcb2b753cc7aa34e78077a8ed133e36fa36008fd6bdfd29af3a5": {
			TrainingScript:   "/app/train_lora.py",
			PriceCoefficient: 8,
			StorageFee:       90000000000000000, // ~800 MB LoRA: 0.04 tokens
		},
	}

	ENV_MAP = map[string][]string{
		"0x02ed6d3889bebad9e2cd4008066478654c0886b12ad25ea7cf7d31df3441182e": {
			"PYTHONPATH=/app/CocktailSGD", // Update to match Python version
		}}
)

const FineTuningDockerfilePath = "/opt/transformer-bridge"

// TODO customer model should upload by user, since:
// 1. tee deployment not allow to mount volume
// 2. user may want to use different model
const ModelUsagePath = "./fine-tuning/execution/models"

// EIP-712 constants matching the contract (FineTuningVerifier.sol)
var (
	// DOMAIN_TYPEHASH = keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	DomainTypehash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))

	// MESSAGE_TYPEHASH = keccak256("VerifierMessage(string id,bytes encryptedSecret,bytes modelRootHash,uint256 nonce,uint256 taskFee,address user)")
	MessageTypehash = crypto.Keccak256Hash([]byte("VerifierMessage(string id,bytes encryptedSecret,bytes modelRootHash,uint256 nonce,uint256 taskFee,address user)"))

	// Domain constants (must match FineTuningVerifier.sol)
	DomainName    = "0G Fine-Tuning Serving"
	DomainVersion = "1"
)
