package constant

var (
	EXECUTION_IMAGE_NAME      = "execution-test-pytorch"
	EXECUTION_MOCK_IMAGE_NAME = "mock-fine-tuning"

	MOCK_MODEL_ROOT_HASH = "0xcb42b5ca9e998c82dd239ef2d20d22a4ae16b3dc0ce0a855c93b52c7c2bab6dc"

	// TODO: For MVP, this is hardcoded to true. In the future, this should can be configurable.
	IS_TURBO = true

	SCRIPT_MAP = map[string]string{
		"0x8645816c17a8a70ebf32bcc7e621c659e8d0150b1a6bfca27f48f83010c6d12e":                                                                    "/app/finetune-img.py",
		"0x7f2244b25cd2219dfd9d14c052982ecce409356e0f08e839b79796e270d110a7":                                                                    "/app/finetune.py",
		"0x2084fdd904c9a3317dde98147d4e7778a40e076b5b0eb469f7a8f27ae5b13e7f":                                                                    "/app/finetune.py",
		"0xcb42b5ca9e998c82dd239ef2d20d22a4ae16b3dc0ce0a855c93b52c7c2bab6dc":                                                                    "/app/finetune.py",
		"0xe25963fd25fe37d7df5216de1eae533ea42090d3642c3f84edd0f179ffc63a94,0xfccaf17bd0ed26b74e8a3883f5c814bcb5f247015d68fd65a28bf98e1bdb0b7f": "/app/CocktailSGD/finetune-cocktail.py",
	}

	ENV_MAP = map[string][]string{
		"0xe25963fd25fe37d7df5216de1eae533ea42090d3642c3f84edd0f179ffc63a94,0xfccaf17bd0ed26b74e8a3883f5c814bcb5f247015d68fd65a28bf98e1bdb0b7f": {
			"PYTHONPATH=/root/miniconda3/envs/cocktail/lib/python3.10/site-packages/:/app/CocktailSGD", // Update to match Python version
			"PATH=/root/miniconda3/envs/cocktail/bin:$PATH",
		}}
)
