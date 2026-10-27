FROM python:3.12-slim
ARG TORCH_INDEX_URL=https://download.pytorch.org/whl/cpu
RUN pip install --no-cache-dir torch==2.7.1 --index-url $TORCH_INDEX_URL
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt
COPY app.py /app/app.py
WORKDIR /app
ENV HF_HOME=/models TOKENIZERS_PARALLELISM=false
CMD ["uvicorn","app:app","--host","0.0.0.0","--port","8081"]
