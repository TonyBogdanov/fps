FROM gcr.io/deeplearning-platform-release/tf2-gpu.2-6:latest

WORKDIR /app
RUN git clone https://github.com/google-research/frame-interpolation /app

#WORKDIR /app/frame-interpolation
#RUN pip install -r requirements.txt
#RUN apt update
#RUN apt install -y ffmpeg
#
#ADD pretrained_models/ /app/pretrained_models/
#CMD [ "bash" ]
