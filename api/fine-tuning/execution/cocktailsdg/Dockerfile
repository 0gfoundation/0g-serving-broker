FROM pytorch/pytorch:2.5.1-cuda12.4-cudnn9-devel

# Update and install essential tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    wget curl git build-essential python3 python3-pip python3-dev \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

RUN update-alternatives --install /usr/bin/python python /usr/bin/python3 1 \
    && update-alternatives --install /usr/bin/pip pip /usr/bin/pip3 1

SHELL ["/bin/bash", "-c"]

WORKDIR /app
RUN git clone https://github.com/0glabs/CocktailSGD.git

# mount point for data
RUN mkdir /app/mnt
COPY transformer/finetune-img.py /app/finetune-img.py
COPY transformer/finetune.py /app/finetune.py

# Install additional Python libraries
#RUN pip install transformers datasets evaluate accelerate toml

ENV PATH="/root/miniconda3/bin:${PATH}"
ARG PATH="/root/miniconda3/bin:${PATH}"

# Install Miniconda on x86 or ARM platforms
RUN arch=$(uname -m) && \
    if [ "$arch" = "x86_64" ]; then \
    MINICONDA_URL="https://repo.anaconda.com/miniconda/Miniconda3-latest-Linux-x86_64.sh"; \
    elif [ "$arch" = "aarch64" ]; then \
    MINICONDA_URL="https://repo.anaconda.com/miniconda/Miniconda3-latest-Linux-aarch64.sh"; \
    else \
    echo "Unsupported architecture: $arch"; \
    exit 1; \
    fi && \
    wget $MINICONDA_URL -O miniconda.sh && \
    mkdir -p /root/.conda && \
    bash miniconda.sh -b -p /root/miniconda3 && \
    rm -f miniconda.sh

RUN conda --version
RUN conda init bash
RUN conda create -n cocktail python=3.10
RUN source activate cocktail

RUN conda install -c "nvidia/label/cuda-11.8.0" cuda-toolkit -n cocktail
RUN conda install pytorch torchvision torchaudio pytorch-cuda=11.8 -c pytorch -c nvidia -n cocktail
RUN conda install -c conda-forge cupy nccl cudatoolkit=11.8 -n cocktail
RUN /root/miniconda3/envs/cocktail/bin/pip install transformers datasets netifaces zstandard wandb numpy evaluate accelerate toml

RUN echo "export PYTHONPATH=$PYTHONPATH:/app/CocktailSGD" >> /root/.bashrc
RUN echo "source activate cocktail" >> /root/.bashrc

CMD ["bash", "-c", "source activate cocktail"]
