# Use PyTorch image with CUDA 12.4 and cuDNN 9
FROM pytorch/pytorch:2.5.1-cuda12.4-cudnn9-devel

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

# Install additional Python libraries 
RUN pip install transformers==4.38.0 datasets evaluate accelerate==0.28.0 toml sentencepiece netifaces zstandard

# Set default command
CMD ["bash"]
