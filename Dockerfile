FROM ubuntu:22.04 AS models

RUN apt-get update && \
    apt-get install -y python3 python3-pip wget unzip && \
    apt-get clean && \
    pip3 install gdown && \
    mkdir /models && \
    cd /models && \
    gdown --folder https://drive.google.com/drive/folders/1q8110-qp225asX3DQvZnfLfJPkCHmDpy

FROM gcr.io/deeplearning-platform-release/tf2-gpu.2-6:latest

WORKDIR /app
RUN git clone https://github.com/TonyBogdanov/frame-interpolation /app && \
    apt-get update && \
    apt-get install -y ffmpeg && \
    apt-get clean && \
    pip install -r requirements.txt

COPY --from=models /models /models
