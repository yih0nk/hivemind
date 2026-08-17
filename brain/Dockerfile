# Hivemind Brain — LangGraph reasoning service.
FROM python:3.11-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    HIVEMIND_BRAIN_PORT=8090

WORKDIR /app

# Install dependencies first so source changes don't bust the layer cache.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY hivemind_brain ./hivemind_brain

# Run as a non-root uid (matches the operator image's 65532).
RUN useradd --uid 65532 --no-create-home --user-group nonroot
USER 65532:65532

EXPOSE 8090
ENTRYPOINT ["python", "-m", "hivemind_brain.server"]
