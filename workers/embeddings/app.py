"""Small local embedder. No documents, keys, or request bodies are logged."""
import hmac
import os
import threading
from fastapi import FastAPI, Header, HTTPException
from pydantic import BaseModel, Field
import torch
from sentence_transformers import SentenceTransformer

MODEL = "intfloat/multilingual-e5-small"
REVISION = "614241f622f53c4eeff9890bdc4f31cfecc418b3"
app = FastAPI()
lock = threading.Lock()
model = None
device = "cpu"


class Batch(BaseModel):
    texts: list[str] = Field(min_length=1, max_length=96)
    model: str = MODEL
    revision: str = REVISION


def authorize(authorization):
    token = os.environ.get("COMPUTE_SERVICE_TOKEN", "")
    if not token or not hmac.compare_digest(authorization or "", "Bearer " + token):
        raise HTTPException(401, "Unauthorized")


@app.get("/health")
def health():
    return {"status": "ready" if model is not None else "warming", "device": device,
            "model": MODEL, "revision": REVISION, "dimensions": 384}


@app.post("/embed")
def embed(batch: Batch, authorization: str | None = Header(default=None)):
    global model, device
    authorize(authorization)
    if batch.model != MODEL or batch.revision != REVISION:
        raise HTTPException(422, "Model revision does not match")
    if any(len(text) > 16000 for text in batch.texts):
        raise HTTPException(422, "A passage is too large")
    with lock:
        if model is None:
            device = "cuda" if torch.cuda.is_available() else "cpu"
            try:
                model = SentenceTransformer(MODEL, revision=REVISION, device=device)
                if device == "cuda":
                    model.half()
            except RuntimeError:
                device = "cpu"
                model = SentenceTransformer(MODEL, revision=REVISION, device="cpu")
        try:
            vectors = model.encode(batch.texts, batch_size=32 if device == "cuda" else 8,
                                   normalize_embeddings=True, convert_to_numpy=True)
        except RuntimeError:
            device = "cpu"
            model = model.float().to("cpu")
            if torch.cuda.is_available():
                torch.cuda.empty_cache()
            vectors = model.encode(batch.texts, batch_size=4, normalize_embeddings=True,
                                   convert_to_numpy=True)
        return {"vectors": vectors.astype("float32").tolist(), "device": device,
                "model": MODEL, "revision": REVISION}
