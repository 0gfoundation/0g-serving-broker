import argparse
import json
import os
import time


class ProgressCallback:
    def __init__(self, log_file_path="/app/mnt/progress.log"):
        self.log_file_path = log_file_path
        self.log_file = None

    def on_train_begin(self):
        try:
            print("Training started...")
            self.log_file = open(self.log_file_path, "a")
        except Exception as e:
            print(f"Error opening log file: {e}")
            exit(1)

    def on_log(self, step, logs=None):
        logs = logs or {}
        log_message = f"Step: {step}, Logs: {logs}\n"
        try:
            self.log_file.write(log_message)
            self.log_file.flush()
        except Exception as e:
            print(f"Error writing to log file: {e}")

    def on_train_end(self):
        print("Training ended.")
        if self.log_file:
            try:
                self.log_file.close()
            except Exception as e:
                print(f"Error closing log file: {e}")


def load_config(config_path):
    try:
        with open(config_path, "r") as f:
            return json.load(f)
    except Exception as e:
        print(f"Error reading config file: {e}")
        exit(1)


def main():
    parser = argparse.ArgumentParser(
        description="Mock the fine-tuning process of a Hugging Face model.")
    parser.add_argument("--data_path", type=str, required=True,
                        help="Path to the dataset (must be locally available).")
    parser.add_argument("--model_path", type=str, required=True,
                        help="Name or path of the pre-trained model.")
    parser.add_argument("--config_path", type=str,
                        default="config.json", help="Path to the config.json file.")
    parser.add_argument("--output_dir", type=str, default="./model_output",
                        help="Directory to save the mocked fine-tuned model.")

    args = parser.parse_args()

    # Load configuration
    config = load_config(args.config_path)

    # Load dataset to simulate using dataset
    # dataset = load_from_disk(args.data_path)

    # Print the simulation setup
    print(f"Mocking fine-tuning with model: {args.model_path}")
    print(f"Dataset loaded from: {args.data_path}")
    print(f"Training configuration: {config}")

    # Initialize ProgressCallback
    callback = ProgressCallback()

    # Simulate the training process
    callback.on_train_begin()
    num_steps = 1  # Simulated number of training steps

    for step in range(num_steps):
        # Log every simulated step
        # Dummy values for logs
        callback.on_log(step, logs={"loss": 0.0, "accuracy": 100.0})
        time.sleep(3)
        print(f"Simulated training step {step + 1}/{num_steps}")

    callback.on_train_end()

    # # Simulate saving the model
    # os.makedirs(args.output_dir, exist_ok=True)
    # output_file = os.path.join(args.output_dir, "mock_model")
    # with open(output_file, "wb") as f:
    #     f.seek((3 * 1024 * 1024 * 1024) - 1)
    #     f.write(b'\0')
    # os.chmod(output_file, 0o666)

    size_in_gb = 3
    size_in_bytes = size_in_gb * 1024 * 1024 * 1024
    chunk_size = 1024 * 1024  # Write in 1 MB chunks

    output_file = os.path.join(args.output_dir, "mock_model")
    with open(output_file, "wb") as f:
        for _ in range(size_in_bytes // chunk_size):
            # Write 1 MB of random data
            f.write(os.urandom(chunk_size))
        # Write any remaining bytes
        remaining_bytes = size_in_bytes % chunk_size
        if remaining_bytes > 0:
            f.write(os.urandom(remaining_bytes))

    # os.makedirs(args.output_dir, exist_ok=True)
    # with open(os.path.join(args.output_dir, "mock_model.txt"), "w") as f:
    #     f.write("This is a mock model. No actual training occurred.\n")


if __name__ == "__main__":
    main()
