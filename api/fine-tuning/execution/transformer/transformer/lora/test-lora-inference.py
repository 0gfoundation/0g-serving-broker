#!/usr/bin/env python3
"""
Qwen2.5-Coder-32B + LoRA 推理测试脚本
测试加载 base 模型 + LoRA adapter 并进行推理
"""

import argparse
import torch
from peft import PeftModel
from transformers import AutoModelForCausalLM, AutoTokenizer


def main():
    parser = argparse.ArgumentParser(description="Qwen2.5-Coder-32B LoRA 推理测试")
    parser.add_argument("--base_model", type=str, required=True, help="Base 模型路径")
    parser.add_argument("--lora_path", type=str, required=True, help="LoRA adapter 路径")
    parser.add_argument("--prompt", type=str, default="用 Python 实现一个快速排序函数", help="测试 prompt")
    parser.add_argument("--max_new_tokens", type=int, default=512, help="最大生成 token 数")

    args = parser.parse_args()

    print("=" * 60)
    print("Qwen2.5-Coder-32B + LoRA 推理测试")
    print("=" * 60)
    print(f"Base 模型: {args.base_model}")
    print(f"LoRA 路径: {args.lora_path}")
    print(f"Prompt: {args.prompt}")
    print("=" * 60)

    # 检查 GPU
    if not torch.cuda.is_available():
        raise RuntimeError("未检测到 GPU！")

    print(f"GPU: {torch.cuda.get_device_name(0)}")
    print("=" * 60)

    # 加载 tokenizer
    print("加载 tokenizer...")
    tokenizer = AutoTokenizer.from_pretrained(
        args.lora_path,  # LoRA 目录中保存了 tokenizer
        trust_remote_code=True,
    )

    # 加载 base 模型
    print("加载 base 模型...")
    print("这可能需要几分钟...")

    base_model = AutoModelForCausalLM.from_pretrained(
        args.base_model,
        torch_dtype=torch.bfloat16,
        device_map="auto",
        trust_remote_code=True,
    )

    print("Base 模型加载完成！")

    # 加载 LoRA adapter
    print(f"加载 LoRA adapter: {args.lora_path}")
    model = PeftModel.from_pretrained(
        base_model,
        args.lora_path,
        torch_dtype=torch.bfloat16,
    )

    print("LoRA adapter 加载完成！")
    print("=" * 60)

    # 合并模式（可选，提升推理速度）
    # model = model.merge_and_unload()

    model.eval()

    # 准备输入
    inputs = tokenizer(args.prompt, return_tensors="pt").to(model.device)

    print("开始生成...")
    print("-" * 60)

    # 生成
    with torch.no_grad():
        start = torch.cuda.Event(enable_timing=True)
        end = torch.cuda.Event(enable_timing=True)

        start.record()
        outputs = model.generate(
            **inputs,
            max_new_tokens=args.max_new_tokens,
            temperature=0.7,
            top_p=0.9,
            do_sample=True,
            pad_token_id=tokenizer.eos_token_id,
        )
        end.record()

        torch.cuda.synchronize()
        elapsed_time = start.elapsed_time(end) / 1000  # 转换为秒

    # 解码
    generated_text = tokenizer.decode(outputs[0], skip_special_tokens=True)

    print("Prompt:")
    print(args.prompt)
    print("-" * 60)
    print("Generated:")
    print(generated_text)
    print("-" * 60)
    print(f"生成时间: {elapsed_time:.2f} 秒")
    print(f"生成 token 数: {len(outputs[0]) - len(inputs.input_ids[0])}")
    print(f"速度: {(len(outputs[0]) - len(inputs.input_ids[0])) / elapsed_time:.2f} tokens/秒")
    print("=" * 60)

    # 显存使用
    max_memory = torch.cuda.max_memory_allocated() / 1024**3
    print(f"最大显存使用: {max_memory:.2f} GB")
    print("=" * 60)


if __name__ == "__main__":
    main()
