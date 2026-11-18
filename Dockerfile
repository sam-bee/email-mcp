FROM python:3.14-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY --chmod=0644 server.py .

USER 10001:10001
EXPOSE 8000
CMD ["python", "server.py"]
