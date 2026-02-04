#!/usr/bin/env python3
"""
LoRA Fine-tuning Script for Qwen and other LLMs
Supports PEFT (Parameter-Efficient Fine-Tuning) with LoRA adapters

Fixed issues:
- Cache directory permissions in containerized environments
- Labels for causal LM loss computation
- Memory-efficient dataset processing
"""

import argparse
import json
import os
import sys
from pathlib import Path

# Set cache directories BEFORE importing transformers/datasets
# This fixes permission issues in containerized environments
os.environ["HF_DATASETS_CACHE"] = "/tmp/hf_cache"
os.environ["TRANSFORMERS_CACHE"] = "/tmp/hf_cache"
os.environ["HF_HOME"] = "/tmp/hf_home"
os.makedirs("/tmp/hf_cache", exist_ok=True)
os.makedirs("/tmp/hf_home", exist_ok=True)

import torch
from datasets import load_dataset, load_from_disk
from transformers import (
    AutoModelForCausalLM,
    AutoTokenizer,
    TrainingArguments,
    Trainer,
    DataCollatorForSeq2Seq,
    BitsAndBytesConfig,
)
from peft import (
    LoraConfig,
    get_peft_model,
    prepare_model_for_kbit_training,
    TaskType,
)


def parse_args():
    parser = argparse.ArgumentParser(description="LoRA Fine-tuning Script")
    parser.add_argument("--model_path", type=str, required=True, help="Path to pre-trained model")
    parser.add_argument("--data_path", type=str, required=True, help="Path to training data")
    parser.add_argument("--output_dir", type=str, required=True, help="Output directory for LoRA weights")
    parser.add_argument("--config_path", type=str, default=None, help="Path to training config JSON")
    return parser.parse_args()


def load_training_config(config_path):
    """Load training configuration from JSON file"""
    default_config = {
        "num_train_epochs": 3,
        "per_device_train_batch_size": 4,
        "gradient_accumulation_steps": 4,
        "learning_rate": 2e-4,
        "max_steps": -1,
        "warmup_ratio": 0.03,
        "warmup_steps": 10,
        "logging_steps": 10,
        "save_steps": 100,
        "save_total_limit": 3,
        "fp16": False,
        "bf16": True,
        "max_seq_length": 512,
        "lora_r": 8,
        "lora_alpha": 32,
        "lora_dropout": 0.1,
        "use_4bit": False,
        "use_8bit": False,
    }
    
    if config_path and os.path.exists(config_path):
        with open(config_path, 'r') as f:
            user_config = json.load(f)
            default_config.update(user_config)
    
    return default_config


def setup_quantization(config):
    """Setup quantization config if needed"""
    if config.get("use_4bit", False):
        return BitsAndBytesConfig(
            load_in_4bit=True,
            bnb_4bit_quant_type="nf4",
            bnb_4bit_compute_dtype=torch.bfloat16,
            bnb_4bit_use_double_quant=True,
        )
    elif config.get("use_8bit", False):
        return BitsAndBytesConfig(load_in_8bit=True)
    return None


def load_model_and_tokenizer(model_path, quantization_config=None):
    """Load model and tokenizer"""
    print(f"Loading model from: {model_path}")
    
    tokenizer = AutoTokenizer.from_pretrained(
        model_path,
        trust_remote_code=True,
        padding_side="right",
    )
    
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token
    
    model_kwargs = {
        "trust_remote_code": True,
        "torch_dtype": torch.bfloat16,
        "device_map": "auto",
    }
    
    if quantization_config:
        model_kwargs["quantization_config"] = quantization_config
    
    model = AutoModelForCausalLM.from_pretrained(model_path, **model_kwargs)
    
    if quantization_config:
        model = prepare_model_for_kbit_training(model)
    
    return model, tokenizer


def setup_lora(model, config):
    """Setup LoRA configuration"""
    # Find target modules based on model architecture
    target_modules = ["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"]
    
    lora_config = LoraConfig(
        r=config.get("lora_r", config.get("r", 8)),
        lora_alpha=config.get("lora_alpha", config.get("alpha", 32)),
        lora_dropout=config.get("lora_dropout", config.get("dropout", 0.1)),
        bias="none",
        task_type=TaskType.CAUSAL_LM,
        target_modules=target_modules,
    )
    
    model = get_peft_model(model, lora_config)
    model.print_trainable_parameters()
    
    return model


def load_and_process_dataset(data_path, tokenizer, max_seq_length):
    """Load and process the training dataset"""
    print(f"Loading dataset from: {data_path}")
    
    # Try different loading methods
    if os.path.isdir(data_path):
        try:
            dataset = load_from_disk(data_path)
            # Handle dataset with splits
            if hasattr(dataset, 'keys') and 'train' in dataset:
                dataset = dataset['train']
        except:
            dataset = load_dataset("json", data_dir=data_path, split="train")
    elif data_path.endswith(".jsonl") or data_path.endswith(".json"):
        dataset = load_dataset("json", data_files=data_path, split="train")
    else:
        dataset = load_dataset(data_path, split="train")
    
    print(f"Dataset loaded: {len(dataset)} samples")
    
    def format_instruction(example):
        """Format the example into instruction format"""
        if "messages" in example:
            # Chat format (Qwen style)
            messages = example["messages"]
            text = tokenizer.apply_chat_template(messages, tokenize=False, add_generation_prompt=False)
        elif "instruction" in example and ("output" in example or "response" in example):
            # Alpaca format
            instruction = example["instruction"]
            input_text = example.get("input", "")
            output = example.get("output", example.get("response", ""))
            
            if input_text:
                text = f"### Instruction:\n{instruction}\n\n### Input:\n{input_text}\n\n### Response:\n{output}"
            else:
                text = f"### Instruction:\n{instruction}\n\n### Response:\n{output}"
        elif "text" in example:
            text = example["text"]
        else:
            # Try to concatenate all string fields
            text = " ".join(str(v) for v in example.values() if isinstance(v, str))
        
        return {"text": text}
    
    # Use keep_in_memory=True to avoid writing cache files (fixes permission issues)
    dataset = dataset.map(format_instruction, remove_columns=dataset.column_names, keep_in_memory=True)
    
    def tokenize_function(examples):
        result = tokenizer(
            examples["text"],
            truncation=True,
            max_length=max_seq_length,
            padding="max_length",
            return_tensors=None,
        )
        # For causal LM, labels are same as input_ids (required for loss computation)
        result["labels"] = result["input_ids"].copy()
        return result
    
    tokenized_dataset = dataset.map(
        tokenize_function,
        batched=True,
        remove_columns=["text"],
        keep_in_memory=True,
    )
    
    return tokenized_dataset


def main():
    args = parse_args()
    
    print("=" * 50)
    print("LoRA Fine-tuning Script")
    print("=" * 50)
    print(f"Model path: {args.model_path}")
    print(f"Data path: {args.data_path}")
    print(f"Output dir: {args.output_dir}")
    print(f"Config path: {args.config_path}")
    print("=" * 50)
    
    # Load configuration
    config = load_training_config(args.config_path)
    print(f"Training config: {json.dumps(config, indent=2)}")
    
    # Setup quantization if needed
    quantization_config = setup_quantization(config)
    
    # Load model and tokenizer
    model, tokenizer = load_model_and_tokenizer(args.model_path, quantization_config)
    
    # Setup LoRA
    model = setup_lora(model, config)
    
    # Load and process dataset
    train_dataset = load_and_process_dataset(
        args.data_path,
        tokenizer,
        config.get("max_seq_length", config.get("max_length", 512))
    )
    
    # Setup training arguments
    training_args = TrainingArguments(
        output_dir=args.output_dir,
        num_train_epochs=config.get("num_train_epochs", config.get("num_epochs", 3)),
        per_device_train_batch_size=config.get("per_device_train_batch_size", config.get("batch_size", 4)),
        gradient_accumulation_steps=config.get("gradient_accumulation_steps", 4),
        learning_rate=config.get("learning_rate", 2e-4),
        max_steps=config.get("max_steps", -1),
        warmup_ratio=config.get("warmup_ratio", 0.03),
        warmup_steps=config.get("warmup_steps", 10),
        logging_steps=config.get("logging_steps", 1),
        save_steps=config.get("save_steps", 100),
        save_total_limit=config.get("save_total_limit", 1),
        fp16=config.get("fp16", False),
        bf16=config.get("bf16", True),
        optim="paged_adamw_32bit" if quantization_config else "adamw_torch",
        lr_scheduler_type="cosine",
        report_to="none",
        gradient_checkpointing=config.get("gradient_checkpointing", False),
        save_safetensors=True,
        dataloader_pin_memory=False,
    )
    
    # Data collator
    data_collator = DataCollatorForSeq2Seq(
        tokenizer=tokenizer,
        padding=True,
        return_tensors="pt",
    )
    
    # Initialize trainer
    trainer = Trainer(
        model=model,
        args=training_args,
        train_dataset=train_dataset,
        data_collator=data_collator,
        tokenizer=tokenizer,
    )
    
    # Train
    print("\nStarting training...")
    trainer.train()
    
    # Save the LoRA model
    print(f"\nSaving LoRA weights to: {args.output_dir}")
    model.save_pretrained(args.output_dir)
    tokenizer.save_pretrained(args.output_dir)
    
    print("\nTraining completed successfully!")
    print(f"LoRA weights saved to: {args.output_dir}")


if __name__ == "__main__":
    main()
