import argparse
import json
import os
from transformers import (
    AutoModelForImageClassification,
    AutoImageProcessor,
    Trainer,
    TrainingArguments,
    AutoConfig,
    TrainerCallback
)
from datasets import load_from_disk
from PIL import Image


class ProgressCallback(TrainerCallback):
    def __init__(self, log_file_path="/app/mnt/progress.log"):
        self.log_file_path = log_file_path
        self.log_file = None

    def on_train_begin(self, args, state, control, **kwargs):
        # Open the log file at the start of training
        try:
            self.log_file = open(self.log_file_path, "a")
        except Exception as e:
            print(f"Error opening log file: {e}")
            exit(1)

    def on_log(self, args, state, control, logs=None, **kwargs):
        logs = logs or {}
        # Only log for the main process in distributed training
        if state.is_local_process_zero:
            log_message = f"Step: {state.global_step}, Logs: {logs}\n"
            try:
                self.log_file.write(log_message)
                self.log_file.flush()  # Ensure the log is written immediately
            except Exception as e:
                print(f"Error writing to log file: {e}")

    def on_train_end(self, args, state, control, **kwargs):
        # Close the log file at the end of training
        if self.log_file:
            try:
                self.log_file.close()
            except Exception as e:
                print(f"Error closing log file: {e}")


def load_config(config_path):
    """Loads configuration from a JSON file."""
    try:
        with open(config_path, "r") as f:
            return json.load(f)
    except Exception as e:
        print(f"Error reading config file: {e}")
        exit(1)


def main():
    parser = argparse.ArgumentParser(description="Fine-tune a Transformer model for image classification.")
    parser.add_argument("--data_path", type=str, required=True,
                        help="Path to the local Hugging Face dataset (saved via 'load_from_disk').")
    parser.add_argument("--model_path", type=str, required=True,
                        help="Name/path of the pre-trained vision model (e.g. 'google/vit-base-patch16-224').")
    parser.add_argument("--config_path", type=str, default="config.json",
                        help="Path to the config.json file with training parameters.")
    parser.add_argument("--output_dir", type=str, default="./model_output",
                        help="Directory to save the fine-tuned model.")

    args = parser.parse_args()

    # Load configuration from JSON file
    config = load_config(args.config_path)

    # Load dataset from disk
    dataset = load_from_disk(args.data_path)

    # Determine the number of labels
    # Assumes 'label' column is an integer class index or a string class name
    if isinstance(dataset["train"][0]["labels"], str):
        # If labels are strings, map them to indices
        labels = list(set(dataset["train"]["labels"]))
        labels.sort()
        label_to_id = {l: i for i, l in enumerate(labels)}
        id_to_label = {i: l for i, l in enumerate(labels)}

        def encode_label(example):
            example["labels"] = label_to_id[example["labels"]]
            return example
        dataset = dataset.map(encode_label)
        n_labels = len(labels)
    else:
        # If labels are already numeric
        n_labels = len(set(dataset["train"]["labels"]))
        label_to_id = None
        id_to_label = None

    # Load the model config, specifying the number of labels
    model_config = AutoConfig.from_pretrained(
        args.model_path,
        num_labels=n_labels,
        finetuning_task="image-classification"
    )

    # (Optional) If you want to ensure your ID/label mappings are stored in the config:
    if label_to_id is not None and id_to_label is not None:
        model_config.label2id = label_to_id
        model_config.id2label = id_to_label

    # Load the image processor (feature extractor)
    image_processor = AutoImageProcessor.from_pretrained(args.model_path, local_files_only=True)

    # Load the pre-trained model for image classification
    model = AutoModelForImageClassification.from_pretrained(
        args.model_path,
        config=model_config,
        local_files_only=True,
        ignore_mismatched_sizes=True
    )

    # Prepare the image-processing function
    def process_images(examples):
        # examples["image"] may already be loaded PIL Images or file paths; adjust as needed
        # If they are file paths, open them:
        if isinstance(examples["image"][0], str):
            images = [Image.open(img_path).convert("RGB") for img_path in examples["image"]]
        else:
            images = [img.convert("RGB") for img in examples["image"]]
        inputs = image_processor(images=images)
        return inputs

    # Process the dataset
    dataset = dataset.map(process_images, batched=True)

    # You can rename/remove columns if necessary, but often HF will ignore them automatically
    # Set format for PyTorch
    dataset.set_format(type="torch", columns=["pixel_values", "labels"])

    # Split into train/validation/test subsets as needed
    # Here, we assume train/validation/test exist in the dataset
    train_dataset = dataset["train"]
    eval_dataset = dataset["validation"] if "validation" in dataset else None
    test_dataset = dataset["test"] if "test" in dataset else None

    # Small subsets for quick debugging (optional)
    small_train_dataset = train_dataset.shuffle(seed=42).select(range(min(1000, len(train_dataset))))
    small_eval_dataset = (
        eval_dataset.shuffle(seed=42).select(range(min(1000, len(eval_dataset))))
        if eval_dataset is not None
        else None
    )

    # Training arguments
    training_args = TrainingArguments(
        output_dir=args.output_dir,
        num_train_epochs=config.get("num_train_epochs", 3),
        per_device_train_batch_size=config.get("per_device_train_batch_size", 8),
        per_device_eval_batch_size=config.get("per_device_eval_batch_size", 8),
        warmup_steps=config.get("warmup_steps", 500),
        weight_decay=config.get("weight_decay", 0.01),
        logging_dir=config.get("logging_dir", "./logs"),
        logging_steps=config.get("logging_steps", 10),
        evaluation_strategy=config.get("evaluation_strategy", "steps"),
        save_strategy=config.get("save_strategy", "steps"),
        save_steps=config.get("save_steps", 500),
        eval_steps=config.get("eval_steps", 500),
        load_best_model_at_end=config.get("load_best_model_at_end", True),
        metric_for_best_model=config.get("metric_for_best_model", "accuracy"),
        greater_is_better=config.get("greater_is_better", True),
        report_to=config.get("report_to", ["none"]),
    )

    # Define the Trainer
    trainer = Trainer(
        model=model,
        args=training_args,
        train_dataset=small_train_dataset,
        eval_dataset=small_eval_dataset,
        tokenizer=None,  # For image tasks, we usually don't need a tokenizer
        callbacks=[ProgressCallback()],
    )

    # Fine-tune the model
    trainer.train()

    # Save the final model
    trainer.save_model(args.output_dir)


if __name__ == "__main__":
    main()
