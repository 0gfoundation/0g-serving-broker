import transformers
import os, sys, json

from datasets import load_from_disk


current_directory = os.path.dirname(os.path.abspath(__file__))
default_model_path = os.path.join(current_directory, "deepseek_v3")


def get_num_train_epochs(training_config):
    num_train_epochs = 3
    if os.path.exists(training_config):
        try:
            with open(training_config, "r") as file:
                config = json.load(file)
                num_train_epochs = config.get("num_train_epochs", 3)
        except Exception as e:
            print(
                f"An error occurred during read training config: {e}", file=sys.stderr
            )

    return num_train_epochs


def count_tokens(dataset_path, model_path, dataset_type):
    if dataset_type == "text":
        encoding = None

        try:
            encoding = transformers.AutoTokenizer.from_pretrained(
                model_path, trust_remote_code=True
            )
        except Exception as e:
            print(f"An error occurred: {e}", file=sys.stderr)

            encoding = transformers.AutoTokenizer.from_pretrained(
                default_model_path, trust_remote_code=True
            )

        dataset = load_from_disk(dataset_path)
        total_tokens = 0
        for _, ds in dataset.items():
            for example in ds:
                for key in ["text", "input", "output"]:
                    if key in example:
                        total_tokens += len(encoding.encode(example[key]))

        return total_tokens
    elif dataset_type == "image":
        dataset = load_from_disk(dataset_path)
        total_tokens = 0

        model_config = transformers.AutoConfig.from_pretrained(
            model_path,
            num_labels=len(set(dataset["train"]["label"])),
            finetuning_task="image-classification",
        )

        patch_size = model_config.patch_size
        image_size = model_config.image_size
        num_patches = (image_size // patch_size) ** 2

        token_size = num_patches + 1

        for _, ds in dataset.items():
            total_tokens += token_size * ds.num_rows

        return total_tokens
    else:
        raise ValueError("Dataset type not supported")


if __name__ == "__main__":
    if len(sys.argv) < 4:
        print(f"Usage: python {sys.argv[0]} <dataset_path> <dateset_type> <model_path>")
        sys.exit(1)

    dataset_path = sys.argv[1]
    dataset_type = sys.argv[2]
    model_path = sys.argv[3]

    num_train_epochs = 0
    if len(sys.argv) == 5:
        training_config = sys.argv[4]
        num_train_epochs = get_num_train_epochs(training_config)

    token_count = count_tokens(dataset_path, model_path, dataset_type)
    print(token_count, num_train_epochs)
