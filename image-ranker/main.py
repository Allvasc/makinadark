"""
Image ranker microservice - picks the stock photo/video-frame candidate that
best matches a scene's text description, using CLIP image-text similarity.

The Go backend used to just take Pexels' first search result unconditionally.
This scores every candidate the backend already fetches (per_page=5) against
the scene's own prompt and returns the best match instead of a blind index 0.

Endpoints:
  GET  /health
  POST /rank-images  {"query": str, "candidates": [str, ...]}  -> {"best_index": int, "scores": [float, ...]}
"""
import io
import logging

import httpx
import open_clip
import torch
from fastapi import FastAPI, HTTPException
from PIL import Image
from pydantic import BaseModel

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("image-ranker")

app = FastAPI(title="MakinaDark Image Ranker")

# ViT-B-32 is the standard "fast on CPU" CLIP checkpoint - a few hundred ms
# per image on a modern CPU, which matters here since this runs per scene
# during video generation, not as a one-off batch job.
_MODEL_NAME = "ViT-B-32"
_PRETRAINED = "laion2b_s34b_b79k"

model = None
preprocess = None
tokenizer = None


@app.on_event("startup")
def load_model():
    global model, preprocess, tokenizer
    logger.info("Loading CLIP model %s (%s)...", _MODEL_NAME, _PRETRAINED)
    model, _, preprocess = open_clip.create_model_and_transforms(_MODEL_NAME, pretrained=_PRETRAINED)
    tokenizer = open_clip.get_tokenizer(_MODEL_NAME)
    model.eval()
    logger.info("CLIP model loaded.")


class RankRequest(BaseModel):
    query: str
    candidates: list[str]  # image URLs (thumbnails - keep these small for speed)


class RankResponse(BaseModel):
    best_index: int
    scores: list[float]


@app.get("/health")
def health():
    return {"status": "healthy", "model_loaded": model is not None}


async def _fetch_image(client: httpx.AsyncClient, url: str) -> Image.Image | None:
    try:
        resp = await client.get(url, timeout=10.0)
        resp.raise_for_status()
        return Image.open(io.BytesIO(resp.content)).convert("RGB")
    except Exception as exc:  # noqa: BLE001 - a single bad candidate must not fail the whole ranking
        logger.warning("Failed to fetch candidate image %s: %s", url, exc)
        return None


@app.post("/rank-images", response_model=RankResponse)
async def rank_images(req: RankRequest):
    if not req.candidates:
        raise HTTPException(status_code=400, detail="candidates must not be empty")
    if model is None:
        raise HTTPException(status_code=503, detail="model not loaded yet")

    async with httpx.AsyncClient() as client:
        images = [await _fetch_image(client, url) for url in req.candidates]

    valid = [(i, img) for i, img in enumerate(images) if img is not None]
    if not valid:
        raise HTTPException(status_code=502, detail="failed to fetch any candidate image")

    with torch.no_grad():
        image_tensors = torch.stack([preprocess(img) for _, img in valid])
        image_features = model.encode_image(image_tensors)
        image_features /= image_features.norm(dim=-1, keepdim=True)

        text_tokens = tokenizer([req.query])
        text_features = model.encode_text(text_tokens)
        text_features /= text_features.norm(dim=-1, keepdim=True)

        similarity = (image_features @ text_features.T).squeeze(-1)

    scores = [0.0] * len(req.candidates)
    for (orig_idx, _), score in zip(valid, similarity.tolist()):
        scores[orig_idx] = score

    best_index = max(range(len(scores)), key=lambda i: scores[i])
    return RankResponse(best_index=best_index, scores=scores)
