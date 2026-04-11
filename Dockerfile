FROM python:3.11-slim

WORKDIR /app

# Install system dependencies for libvirt and psutil
# We use a multi-stage approach or just ensure all build deps are present
RUN apt-get update && apt-get install -y \
    gcc \
    python3-dev \
    libvirt-dev \
    libxml2-dev \
    libxslt1-dev \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .

CMD ["python", "server.py"]
