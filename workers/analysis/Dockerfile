FROM python:3.12-slim
RUN pip install --no-cache-dir pandas==2.2.3 numpy==2.2.6 matplotlib==3.10.3
COPY runner.py /runner.py
ENV MPLCONFIGDIR=/tmp/matplotlib PYTHONDONTWRITEBYTECODE=1 OPENBLAS_NUM_THREADS=1
USER 65532:65532
ENTRYPOINT ["python","-I","/runner.py"]
