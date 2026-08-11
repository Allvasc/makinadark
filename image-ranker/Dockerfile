FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
# --extra-index-url (not --index-url) so pip still resolves everything else
# from regular PyPI, but prefers the CPU-only wheels for torch/torchvision -
# installing torch and open_clip_torch in two separate pip calls let the
# second call silently replace the first's CPU build with the default CUDA
# one (~5GB of unused nvidia-cuda-* packages), so this has to happen in a
# single resolution pass instead.
RUN pip install --no-cache-dir -r requirements.txt --extra-index-url https://download.pytorch.org/whl/cpu

COPY main.py .

# Bake the CLIP weights into the image at build time instead of downloading
# on first request - keeps the container's first real request fast and
# avoids a network dependency at runtime.
RUN python -c "import open_clip; open_clip.create_model_and_transforms('ViT-B-32', pretrained='laion2b_s34b_b79k')"

EXPOSE 8500
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8500"]
