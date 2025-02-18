import argparse
import json
import os
from transformers import AutoModelForSequenceClassification, AutoTokenizer, Trainer, TrainingArguments, AutoConfig, TrainerCallback
from datasets import load_dataset, load_from_disk


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
        if state.is_local_process_zero:  # Only log for the main process in distributed training
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
    parser = argparse.ArgumentParser(description="Fine-tune a Hugging Face model.")
    parser.add_argument("--data_path", type=str, required=True, help="Name of the dataset (Hugging Face hub).")
    parser.add_argument("--model_path", type=str, required=True, help="Name of the pre-trained model.")
    parser.add_argument("--config_path", type=str, default="config.json", help="Path to the config.json file.")
    parser.add_argument("--output_dir", type=str, default="./model_output", help="Directory to save the fine-tuned model.")

    args = parser.parse_args()

    # Load configuration from JSON file
    config = load_config(args.config_path)

    # Load dataset
    dataset = load_from_disk(args.data_path)

    n_labels = len(set(dataset["train"]["label"]))
    model_config = AutoConfig.from_pretrained(args.model_path, num_labels=n_labels)  # Adjust `num_labels` as needed.

    # Load tokenizer and model
    tokenizer = AutoTokenizer.from_pretrained(args.model_path, local_files_only=True)
    model = AutoModelForSequenceClassification.from_pretrained(
        args.model_path, config=model_config, local_files_only=True
    )

    # Tokenize the dataset
    def tokenize_function(examples):
        return tokenizer(examples["text"], padding="max_length", max_length=config.get("max_length", 200), truncation=True)

    tokenized_datasets = dataset.map(tokenize_function, batched=True)

    # Prepare train and validation datasets
    train_dataset = tokenized_datasets["train"]
    eval_dataset = tokenized_datasets["validation"] if "validation" in tokenized_datasets else None

    small_train_dataset = tokenized_datasets["train"].shuffle(seed=42).select(range(1000))
    small_eval_dataset = tokenized_datasets["test"].shuffle(seed=42).select(range(1000))

    # Training arguments from config file
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
        tokenizer=tokenizer,
        callbacks=[ProgressCallback()],
    )

    # Fine-tune the model
    trainer.train()

    # Save the model
    trainer.save_model(args.output_dir)


if __name__ == "__main__":
    main()
