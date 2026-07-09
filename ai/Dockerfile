# Python 3.11 for broad ML-ecosystem compatibility (Phase 2 adds torch etc.).
FROM python:3.11-slim

WORKDIR /app

# System deps kept minimal for Phase 0.
RUN pip install --no-cache-dir --upgrade pip

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

EXPOSE 8000
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
