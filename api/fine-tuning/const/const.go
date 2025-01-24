package constant

var (
	EXECUTION_IMAGE_NAME      = "execution-test-pytorch"
	EXECUTION_MOCK_IMAGE_NAME = "mock-fine-tuning"

	MOCK_MODEL_ROOT_HASH = "0xf463fe8c26e7dbca20716eb3c81ac1f3ea23a6c5dbe002bf46507db403c71578"

	// TODO: For MVP, this is hardcoded to true. In the future, this should can be configurable.
	IS_TURBO = true

	SCRIPT_MAP = map[string]string{
		"0x8645816c17a8a70ebf32bcc7e621c659e8d0150b1a6bfca27f48f83010c6d12e": "/app/finetune_img.py",
		"0x7f2244b25cd2219dfd9d14c052982ecce409356e0f08e839b79796e270d110a7": "/app/finetune.py",
	}
)
