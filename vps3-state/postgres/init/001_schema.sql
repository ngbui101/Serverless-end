CREATE TABLE IF NOT EXISTS processed_images (
    image_id UUID PRIMARY KEY,
    original_filename TEXT NOT NULL,
    minio_processed_url TEXT NOT NULL,
    processing_time_ms BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_processed_images_created_at
    ON processed_images (created_at DESC);

