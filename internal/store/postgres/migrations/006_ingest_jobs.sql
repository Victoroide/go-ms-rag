ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS fingerprint TEXT,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS ingest_jobs (
    document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running')),
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ingest_jobs_ready ON ingest_jobs (next_attempt_at) WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS idx_ingest_jobs_lease ON ingest_jobs (lease_until) WHERE state = 'running';
UPDATE documents d SET status = 'completed', completed_at = COALESCE(d.completed_at, NOW())
 WHERE d.status = 'processing'
   AND EXISTS (SELECT 1 FROM document_fingerprints f WHERE f.document_id = d.id);
INSERT INTO ingest_jobs (document_id)
SELECT d.id FROM documents d WHERE d.status = 'processing'
ON CONFLICT (document_id) DO NOTHING;
