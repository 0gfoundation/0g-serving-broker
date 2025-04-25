package constant

var (
	MOCK_MODEL_ROOT_HASH = "0xcb42b5ca9e998c82dd239ef2d20d22a4ae16b3dc0ce0a855c93b52c7c2bab6dc"

	// TODO: For MVP, this is hardcoded to true. In the future, this should can be configurable.
	IS_TURBO = true

	SCRIPT_MAP = map[string]string{
		"0x8645816c17a8a70ebf32bcc7e621c659e8d0150b1a6bfca27f48f83010c6d12e": "/app/finetune-img.py",
		"0x7f2244b25cd2219dfd9d14c052982ecce409356e0f08e839b79796e270d110a7": "/app/finetune.py",
		"0x2084fdd904c9a3317dde98147d4e7778a40e076b5b0eb469f7a8f27ae5b13e7f": "/app/finetune.py",
		"0xcb42b5ca9e998c82dd239ef2d20d22a4ae16b3dc0ce0a855c93b52c7c2bab6dc": "/app/finetune.py",
		"0x02ed6d3889bebad9e2cd4008066478654c0886b12ad25ea7cf7d31df3441182e": "/app/CocktailSGD/finetune-cocktail.py",
	}

	ENV_MAP = map[string][]string{
		"0x02ed6d3889bebad9e2cd4008066478654c0886b12ad25ea7cf7d31df3441182e": {
			"PYTHONPATH=/app/CocktailSGD", // Update to match Python version
		}}
)

const FineTuningDockerfilePath = "./fine-tuning/execution/transformer"
