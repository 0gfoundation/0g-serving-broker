# Use PyTorch image with CUDA 12.4 and cuDNN 9
FROM pytorch/pytorch:2.5.1-cuda12.4-cudnn9-devel

ENV DEBIAN_FRONTEND=noninteractive

# Update and install essential tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    wget curl git build-essential python3 python3-pip python3-dev \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

RUN update-alternatives --install /usr/bin/python python /usr/bin/python3 1 \
    && update-alternatives --install /usr/bin/pip pip /usr/bin/pip3 1

RUN conda install -c conda-forge cupy

WORKDIR /app

RUN git clone https://github.com/0glabs/CocktailSGD.git

# mount point for data
RUN mkdir /app/mnt
COPY transformer/finetune-img.py /app/finetune-img.py
COPY transformer/finetune.py /app/finetune.py
COPY transformer/train_lora.py /app/train_lora.py

# Install Python libraries with compatible versions
# Note: Pin numpy<2.0.0 for pyarrow compatibility
#       Pin datasets and pyarrow to avoid version conflicts
#       Use transformers>=4.51.0 for Qwen3 support
#       Use accelerate>=0.30.0 for transformers 5.x compatibility
RUN pip install --upgrade pip setuptools wheel && \
    pip install --no-cache-dir \
    "numpy<2.0.0" \
    "transformers>=4.51.0" \
    datasets==2.19.0 \
    pyarrow==14.0.2 \
    "accelerate>=0.30.0" \
    "peft>=0.11.0" \
    evaluate \
    toml \
    sentencepiece \
    netifaces \
    zstandard \
    bitsandbytes \
    scipy \
    protobuf \
    safetensors \
    huggingface_hub

# Set default command
CMD ["bash"]
