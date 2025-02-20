import transformers
import os, sys

from datasets import load_from_disk


current_directory = os.path.dirname(os.path.abspath(__file__))
default_model_path = os.path.join(current_directory, "deepseek_v3")


def count_tokens(dataset_path, chat_tokenizer_dir):
    encoding = None

    try:
        encoding = transformers.AutoTokenizer.from_pretrained(
            chat_tokenizer_dir, trust_remote_code=True
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
            text = example["text"]
            total_tokens += len(encoding.encode(text))

    return total_tokens


if __name__ == "__main__":
    dataset_path = sys.argv[1]
    model_name = sys.argv[2] if len(sys.argv) > 2 else default_model_path
    token_count = count_tokens(dataset_path, model_name)
    print(token_count)
