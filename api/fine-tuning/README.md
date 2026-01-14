# Fine-tuning Development Guide

## Local Development Setup

### Python Environment

The token counting functionality requires Python with specific dependencies. Set up a local virtual environment:

```bash
cd api

# Create virtual environment
python3 -m venv .venv

# Activate virtual environment
source .venv/bin/activate

# Install dependencies
pip install -r requirements.txt
```

Deactivate the virtual environment:
```bash
deactivate
```

### Running Token Counter

```bash
source .venv/bin/activate
python3 token-counter/token_counter.py <dataset_path> <dataset_type> <model_path> [training_config]
```