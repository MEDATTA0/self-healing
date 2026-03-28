FROM python:3.12-slim

WORKDIR /app

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application source and models
COPY api.py .
COPY models/ ./models/

EXPOSE 8010

CMD ["uvicorn", "api:app", "--host", "0.0.0.0", "--port", "8010"]
