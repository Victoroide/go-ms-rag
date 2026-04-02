DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name='documents' AND column_name='doc_type'
    ) THEN
        ALTER TABLE documents ADD COLUMN doc_type TEXT NOT NULL DEFAULT 'candidate';
    END IF;
END $$;

-- Backfill doc_type from metadata JSONB for existing documents
UPDATE documents SET doc_type = metadata->>'doc_type' 
WHERE metadata->>'doc_type' IS NOT NULL AND doc_type = 'candidate';
